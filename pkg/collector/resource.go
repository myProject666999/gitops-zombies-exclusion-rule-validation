// Package collector provides functionality for discovering and collecting Kubernetes resources.
package collector

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	helmapi "github.com/fluxcd/helm-controller/api/v2"
	ksapi "github.com/fluxcd/kustomize-controller/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
	"sigs.k8s.io/cli-utils/pkg/object"

	v1 "github.com/raffis/gitops-zombies/pkg/apis/gitopszombies/v1"
)

const (
	fluxHelmNameLabel           = "helm.toolkit.fluxcd.io/name"
	fluxHelmNamespaceLabel      = "helm.toolkit.fluxcd.io/namespace"
	fluxKustomizeNameLabel      = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizeNamespaceLabel = "kustomize.toolkit.fluxcd.io/namespace"
)

// FilterFunc is a function that filters resources.
type FilterFunc func(res unstructured.Unstructured, logger klog.Logger) bool

// Interface represents collector interface.
type Interface interface {
	Discover(ctx context.Context, list *unstructured.UnstructuredList, ch chan unstructured.Unstructured) error
}

type discovery struct {
	filters []FilterFunc
	logger  klog.Logger
}

// NewDiscovery returns a new discovery instance.
func NewDiscovery(logger klog.Logger, filters ...FilterFunc) Interface {
	return &discovery{
		logger:  logger,
		filters: filters,
	}
}

// Discover validates discovered resources against all filters and adds it to consumer channel.
func (d *discovery) Discover(
	_ context.Context,
	list *unstructured.UnstructuredList,
	ch chan unstructured.Unstructured,
) error {
RESOURCES:
	for _, res := range list.Items {
		d.logger.V(1).Info("validate resource", "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion())

		for _, filter := range d.filters {
			if filter(res, d.logger) {
				continue RESOURCES
			}
		}

		ch <- res
	}

	return nil
}

// IgnoreOwnedResource returns a FilterFunc which filters resources owner by parents ones.
func IgnoreOwnedResource() FilterFunc {
	return func(res unstructured.Unstructured, logger klog.Logger) bool {
		if refs := res.GetOwnerReferences(); len(refs) > 0 {
			logger.V(1).
				Info("ignore resource owned by parent", "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion())
			return true
		}

		return false
	}
}

// IgnoreServiceAccountSecret returns a FilterFunc which filters secrets linked to a service account.
func IgnoreServiceAccountSecret() FilterFunc {
	return func(res unstructured.Unstructured, _ klog.Logger) bool {
		if res.GetKind() == "Secret" && res.GetAPIVersion() == "v1" {
			if _, ok := res.GetAnnotations()["kubernetes.io/service-account.name"]; ok {
				return true
			}
		}

		return false
	}
}

// IgnoreHelmSecret returns a FilterFunc which filters secrets owned by helm.
func IgnoreHelmSecret() FilterFunc {
	return func(res unstructured.Unstructured, _ klog.Logger) bool {
		if res.GetKind() == "Secret" && res.GetAPIVersion() == "v1" {
			if v, ok := res.GetLabels()["owner"]; ok && v == "helm" {
				return true
			}
		}

		return false
	}
}

// IgnoreIfHelmReleaseFound returns a FilterFunc which filters resources part of an helm release.
func IgnoreIfHelmReleaseFound(helmReleases []helmapi.HelmRelease) FilterFunc {
	return func(res unstructured.Unstructured, logger klog.Logger) bool {
		labels := res.GetLabels()
		if helmName, ok := labels[fluxHelmNameLabel]; ok {
			if helmNamespace, ok := labels[fluxHelmNamespaceLabel]; ok {
				if hasResource(helmReleases, helmName, helmNamespace) {
					return true
				}

				logger.V(1).
					Info("helmrelease not found from resource", "helmReleaseName", helmName, "helmReleaseNamespace", helmNamespace, "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion())
			}
		}

		return false
	}
}

// IgnoreIfKustomizationFound returns a FilterFunc which filters resources part of a flux kustomization.
func IgnoreIfKustomizationFound(kustomizations []ksapi.Kustomization) FilterFunc {
	return func(res unstructured.Unstructured, logger klog.Logger) bool {
		labels := res.GetLabels()
		ksName, okKsName := labels[fluxKustomizeNameLabel]
		ksNamespace, okKsNamespace := labels[fluxKustomizeNamespaceLabel]
		if !okKsName || !okKsNamespace {
			return false
		}

		if ks := findKustomization(kustomizations, ksName, ksNamespace); ks != nil {
			obj := object.ObjMetadata{
				Namespace: res.GetNamespace(),
				Name:      res.GetName(),
				GroupKind: res.GroupVersionKind().GroupKind(),
			}
			id := obj.String()

			logger.V(1).
				Info("lookup kustomization inventory", "kustomizationName", ksName, "kustomizationNamespace", ksNamespace, "resourceId", id)

			if ks.Status.Inventory != nil {
				for _, entry := range ks.Status.Inventory.Entries {
					if entry.ID == id {
						return true
					}
				}
			}

			logger.V(1).
				Info("resource is not part of the kustomization inventory", "name", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion(), "kustomizationName", ksName, "kustomizationNamespace", ksNamespace)
			return false
		}
		logger.V(1).
			Info("kustomization not found from resource", "resource", res.GetName(), "namespace", res.GetNamespace(), "apiVersion", res.GetAPIVersion(), "kustomizationName", ksName, "kustomizationNamespace", ksNamespace)
		return false
	}
}

// ErrEmptyExclusionRule is returned for an excludeResources rule whose fields are all empty. Such a rule
// would match every resource and silently disable zombie detection, therefore it is rejected. Use an
// explicit match-all pattern such as `name: ".*"` instead.
var ErrEmptyExclusionRule = errors.New(
	`rule is empty and would exclude every resource; use an explicit match-all pattern such as name: ".*"`,
)

// ExclusionRuleError describes a single problem in a single excludeResources rule.
type ExclusionRuleError struct {
	// RuleIndex is the 1-based position of the rule in excludeResources.
	RuleIndex int
	// Field is the offending field, e.g. "cluster", "name" or `labels[app]`. It is empty for a rule
	// which is rejected as a whole.
	Field string
	// Expression is the original, unmodified user supplied expression.
	Expression string
	// Err is the underlying cause, if any.
	Err error
}

// Error implements the error interface.
func (e *ExclusionRuleError) Error() string {
	prefix := fmt.Sprintf("excludeResources[%d]", e.RuleIndex)
	if e.Field != "" {
		prefix += "." + e.Field
	}

	if e.Expression != "" {
		return fmt.Sprintf("%s: invalid regular expression %q: %v", prefix, e.Expression, e.Err)
	}

	return fmt.Sprintf("%s: %v", prefix, e.Err)
}

// Unwrap returns the underlying cause.
func (e *ExclusionRuleError) Unwrap() error {
	return e.Err
}

// ExclusionRuleErrors aggregates every problem found while compiling excludeResources rules.
type ExclusionRuleErrors []*ExclusionRuleError

// Error implements the error interface.
func (e ExclusionRuleErrors) Error() string {
	messages := make([]string, 0, len(e))
	for _, ruleErr := range e {
		messages = append(messages, ruleErr.Error())
	}

	return strings.Join(messages, "; ")
}

// Unwrap returns the individual rule errors.
func (e ExclusionRuleErrors) Unwrap() []error {
	errs := make([]error, 0, len(e))
	for _, ruleErr := range e {
		errs = append(errs, ruleErr)
	}

	return errs
}

// ExclusionRule is a validated and precompiled excludeResources rule.
//
// The fields cluster, namespace, name and the values of annotations and labels are user supplied regular
// expressions. They are anchored implicitly: a pattern p is compiled as `^(?:p)$` and therefore has to match
// the complete value. apiVersion and kind are compared literally and are not regular expressions.
//
// A nil pattern for cluster, namespace or name matches everything. A nil pattern for a label or annotation
// value only matches an empty value, mirroring the previous `^$` behaviour. The rules are immutable after
// compilation and safe for concurrent use.
type ExclusionRule struct {
	cluster     *regexp.Regexp
	namespace   *regexp.Regexp
	name        *regexp.Regexp
	annotations map[string]*regexp.Regexp
	labels      map[string]*regexp.Regexp
	apiVersion  string
	kind        string
}

// CompileExclusions validates and precompiles all excludeResources rules.
//
// Every pattern is compiled exactly once. Problems are not logged but returned: all problems of all rules
// are reported in rule order, and a single rule may contribute more than one error. Exclusive rule fields
// are checked in the order cluster, namespace, name, annotations (sorted by key), labels (sorted by key).
func CompileExclusions(exclusions []v1.ExcludeResources) ([]ExclusionRule, error) {
	rules := make([]ExclusionRule, 0, len(exclusions))
	errs := ExclusionRuleErrors{}

	for idx, exclusion := range exclusions {
		if isEmptyExclusion(exclusion) {
			errs = append(errs, &ExclusionRuleError{
				RuleIndex: idx + 1,
				Err:       ErrEmptyExclusionRule,
			})
			continue
		}

		rule, ruleErrs := compileExclusion(idx+1, exclusion)
		if len(ruleErrs) > 0 {
			errs = append(errs, ruleErrs...)
			continue
		}

		rules = append(rules, rule)
	}

	if len(errs) > 0 {
		return nil, errs
	}

	return rules, nil
}

// IgnoreRuleExclusions returns a FilterFunc which excludes resources matching one of the precompiled rules.
// Rules must be created with CompileExclusions so that patterns are validated and compiled only once.
func IgnoreRuleExclusions(cluster string, rules []ExclusionRule) FilterFunc {
	return func(res unstructured.Unstructured, _ klog.Logger) bool {
		for i := range rules {
			if rules[i].matches(cluster, res) {
				return true
			}
		}

		return false
	}
}

func (r *ExclusionRule) matches(cluster string, res unstructured.Unstructured) bool {
	switch {
	case r.cluster != nil && !r.cluster.MatchString(cluster):
		return false
	case r.apiVersion != "" && res.GetAPIVersion() != r.apiVersion:
		return false
	case r.kind != "" && res.GetKind() != r.kind:
		return false
	case r.namespace != nil && !r.namespace.MatchString(res.GetNamespace()):
		return false
	case !metadataMatches(res.GetAnnotations(), r.annotations):
		return false
	case !metadataMatches(res.GetLabels(), r.labels):
		return false
	case r.name != nil && !r.name.MatchString(res.GetName()):
		return false
	}

	return true
}

func isEmptyExclusion(exclusion v1.ExcludeResources) bool {
	return exclusion.Cluster == "" &&
		exclusion.Namespace == "" &&
		exclusion.Name == "" &&
		len(exclusion.Annotations) == 0 &&
		len(exclusion.Labels) == 0 &&
		exclusion.APIVersion == "" &&
		exclusion.Kind == ""
}

func compileExclusion(ruleIndex int, exclusion v1.ExcludeResources) (ExclusionRule, ExclusionRuleErrors) {
	rule := ExclusionRule{
		apiVersion: exclusion.APIVersion,
		kind:       exclusion.Kind,
	}

	var errs ExclusionRuleErrors

	compile := func(field, expression string) *regexp.Regexp {
		if expression == "" {
			return nil
		}

		pattern, err := compilePattern(expression)
		if err != nil {
			errs = append(errs, &ExclusionRuleError{
				RuleIndex:  ruleIndex,
				Field:      field,
				Expression: expression,
				Err:        err,
			})
			return nil
		}

		return pattern
	}

	rule.cluster = compile("cluster", exclusion.Cluster)
	rule.namespace = compile("namespace", exclusion.Namespace)
	rule.name = compile("name", exclusion.Name)
	rule.annotations = compileMetadata(ruleIndex, "annotations", exclusion.Annotations, &errs)
	rule.labels = compileMetadata(ruleIndex, "labels", exclusion.Labels, &errs)

	return rule, errs
}

func compileMetadata(
	ruleIndex int,
	field string,
	metadata map[string]string,
	errs *ExclusionRuleErrors,
) map[string]*regexp.Regexp {
	if len(metadata) == 0 {
		return nil
	}

	patterns := make(map[string]*regexp.Regexp, len(metadata))

	for _, key := range slices.Sorted(maps.Keys(metadata)) {
		expression := metadata[key]
		if expression == "" {
			// An empty expression is equivalent to the old `^$` pattern: it matches only an empty value.
			patterns[key] = nil
			continue
		}

		pattern, err := compilePattern(expression)
		if err != nil {
			*errs = append(*errs, &ExclusionRuleError{
				RuleIndex:  ruleIndex,
				Field:      fmt.Sprintf("%s[%s]", field, key),
				Expression: expression,
				Err:        err,
			})
			continue
		}

		patterns[key] = pattern
	}

	return patterns
}

// compilePattern anchors the user expression with a non-capturing group so that alternations cannot escape
// the implicit anchors. The expression must not be empty; callers handle the empty case themselves.
func compilePattern(expression string) (*regexp.Regexp, error) {
	return regexp.Compile("^(?:" + expression + ")$")
}

func metadataMatches(resMetadata map[string]string, patterns map[string]*regexp.Regexp) bool {
	for key, pattern := range patterns {
		value, ok := resMetadata[key]
		if !ok {
			return false
		}

		// A nil pattern stems from an empty expression and matches only an empty value.
		if pattern == nil {
			if value != "" {
				return false
			}

			continue
		}

		if !pattern.MatchString(value) {
			return false
		}
	}

	return true
}

func hasResource(pool []helmapi.HelmRelease, name, namespace string) bool {
	for _, res := range pool {
		if res.GetName() == name && res.GetNamespace() == namespace {
			return true
		}
	}

	return false
}

func findKustomization(pool []ksapi.Kustomization, name, namespace string) *ksapi.Kustomization {
	for _, res := range pool {
		if res.GetName() == name && res.GetNamespace() == namespace {
			return &res
		}
	}

	return nil
}
