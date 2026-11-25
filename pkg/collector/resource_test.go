package collector

import (
	"bytes"
	"os"
	"strings"
	"testing"

	helmapi "github.com/fluxcd/helm-controller/api/v2"
	ksapi "github.com/fluxcd/kustomize-controller/api/v1"
	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	gitopszombiesv1 "github.com/raffis/gitops-zombies/pkg/apis/gitopszombies/v1"
)

type NullLogger struct{}

func (l NullLogger) Debugf(_ string, _ ...any) {
}

func (l NullLogger) Failuref(_ string, _ ...any) {
}

type test struct {
	name         string
	filters      func(_ *testing.T) []FilterFunc
	list         func() *unstructured.UnstructuredList
	expectedPass int
}

func getExclusionListResourceSet() *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{}

	res1 := unstructured.Unstructured{}
	res1.SetName("velero-capi-backup-1")
	res1.SetNamespace("velero")
	res1.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "velero.io",
		Version: "v1",
		Kind:    "Backup",
	})
	res1.SetAnnotations(map[string]string{"test-annotation": "velero-capi-backup-1"})

	res2 := unstructured.Unstructured{}
	res2.SetName("velero-capi-backup-2")
	res2.SetNamespace("velero")
	res2.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "velero.io",
		Version: "v2",
		Kind:    "Backup",
	})
	res2.SetLabels(map[string]string{"test-label": "velero-capi-backup-2"})

	res3 := unstructured.Unstructured{}
	res3.SetName("velero-capi-backup-3")
	res3.SetNamespace("velero2")
	res3.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "velero.io",
		Version: "v1",
		Kind:    "Backuped",
	})

	list.Items = append(list.Items, res1, res2, res3)

	return list
}

func TestDiscovery(t *testing.T) {
	tests := []test{
		{
			name: "A resource which has owner references is skipped",
			filters: func(_ *testing.T) []FilterFunc {
				return []FilterFunc{IgnoreOwnedResource()}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("resource-without-owner")

				notExpected := unstructured.Unstructured{}
				notExpected.SetName("resource-with-owner")
				notExpected.SetOwnerReferences([]v1.OwnerReference{
					{
						Name: "owner",
					},
				})

				list.Items = append(list.Items, expected, notExpected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A secret which belongs to a service account is ignored",
			filters: func(_ *testing.T) []FilterFunc {
				return []FilterFunc{IgnoreServiceAccountSecret()}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("secret")
				expected.SetAPIVersion("v1")
				expected.SetKind("Secret")

				notExpected := unstructured.Unstructured{}
				notExpected.SetName("service-account-secret")
				notExpected.SetAPIVersion("v1")
				notExpected.SetKind("Secret")
				notExpected.SetAnnotations(map[string]string{
					"kubernetes.io/service-account.name": "sa",
				})

				list.Items = append(list.Items, expected, notExpected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A secret which is labeled as a helm owner is ignored",
			filters: func(_ *testing.T) []FilterFunc {
				return []FilterFunc{IgnoreHelmSecret()}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("secret")
				expected.SetAPIVersion("v1")
				expected.SetKind("Secret")

				notExpected := unstructured.Unstructured{}
				notExpected.SetName("service-account-secret")
				notExpected.SetAPIVersion("v1")
				notExpected.SetKind("Secret")
				notExpected.SetLabels(map[string]string{
					"owner": "helm",
				})

				list.Items = append(list.Items, expected, notExpected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "A resource which is part of a helmrelease is ignored",
			filters: func(_ *testing.T) []FilterFunc {
				helmReleases := []helmapi.HelmRelease{}
				hr := helmapi.HelmRelease{}
				hr.SetName("release")
				hr.SetNamespace("test")

				helmReleases = append(helmReleases, hr)

				return []FilterFunc{IgnoreIfHelmReleaseFound(helmReleases)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("resource")

				alsoExpected := unstructured.Unstructured{}
				alsoExpected.SetName("service-account-secret")
				alsoExpected.SetLabels(map[string]string{
					fluxHelmNameLabel:      "release",
					fluxHelmNamespaceLabel: "not-existing",
				})

				notExpected := unstructured.Unstructured{}
				notExpected.SetName("service-account-secret")
				notExpected.SetLabels(map[string]string{
					fluxHelmNameLabel:      "release",
					fluxHelmNamespaceLabel: "test",
				})

				list.Items = append(list.Items, expected, alsoExpected, notExpected)
				return list
			},
			expectedPass: 2,
		},
		{
			name: "A resource which is part of a kustomization but without a matching inventory entry is not ignored",
			filters: func(_ *testing.T) []FilterFunc {
				kustomizations := &ksapi.KustomizationList{}
				ks := ksapi.Kustomization{}
				ks.SetName("release")
				ks.SetNamespace("test")

				kustomizations.Items = append(kustomizations.Items, ks)

				return []FilterFunc{IgnoreIfKustomizationFound(kustomizations.Items)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("resource")

				alsoExpected := unstructured.Unstructured{}
				alsoExpected.SetName("service-account-secret")
				alsoExpected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "release",
					fluxKustomizeNamespaceLabel: "test",
				})

				list.Items = append(list.Items, expected, alsoExpected)
				return list
			},
			expectedPass: 2,
		},
		{
			name: "A resource which is part of a kustomization and has a valid matching inventory entry is ignored",
			filters: func(_ *testing.T) []FilterFunc {
				kustomizations := &ksapi.KustomizationList{}
				ks := ksapi.Kustomization{}
				ks.SetName("release")
				ks.SetNamespace("test")
				ks.Status.Inventory = &ksapi.ResourceInventory{
					Entries: []ksapi.ResourceRef{
						{
							ID: "test_cluster-role__test_rbac.authorization.k8s.io_ClusterRole",
						},
					},
				}

				kustomizations.Items = append(kustomizations.Items, ks)

				return []FilterFunc{IgnoreIfKustomizationFound(kustomizations.Items)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("resource")

				alsoExpected := unstructured.Unstructured{}
				alsoExpected.SetName("service-account-secret")
				alsoExpected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "release",
					fluxKustomizeNamespaceLabel: "test",
				})

				notExpected := unstructured.Unstructured{}
				notExpected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "rbac.authorization.k8s.io",
					Version: "v1",
					Kind:    "ClusterRole",
				})
				notExpected.SetNamespace("test")
				notExpected.SetName("cluster-role:test")
				notExpected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "release",
					fluxKustomizeNamespaceLabel: "test",
				})

				list.Items = append(list.Items, expected, alsoExpected, notExpected)
				return list
			},
			expectedPass: 2,
		},
		{
			name: "A resource which is part of a kustomization but the kustomization was not found",
			filters: func(_ *testing.T) []FilterFunc {
				kustomizations := &ksapi.KustomizationList{}
				ks := ksapi.Kustomization{}
				ks.SetName("release")
				ks.SetNamespace("test")
				ks.Status.Inventory = &ksapi.ResourceInventory{
					Entries: []ksapi.ResourceRef{
						{
							ID: "test_service-account-secret__Secret",
						},
					},
				}

				kustomizations.Items = append(kustomizations.Items, ks)

				return []FilterFunc{IgnoreIfKustomizationFound(kustomizations.Items)}
			},
			list: func() *unstructured.UnstructuredList {
				list := &unstructured.UnstructuredList{}
				expected := unstructured.Unstructured{}
				expected.SetName("service-account-secret")
				expected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "does-not-exists",
					fluxKustomizeNamespaceLabel: "does-not-exists",
				})

				notExpected := unstructured.Unstructured{}
				notExpected.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "",
					Version: "v1",
					Kind:    "Secret",
				})
				notExpected.SetNamespace("test")
				notExpected.SetName("service-account-secret")
				notExpected.SetLabels(map[string]string{
					fluxKustomizeNameLabel:      "release",
					fluxKustomizeNamespaceLabel: "test",
				})

				list.Items = append(list.Items, expected, notExpected)
				return list
			},
			expectedPass: 1,
		},
		{
			name: "Resources excluded from conf: match all",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Name: ".*",
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 0,
		},
		{
			name: "Resources excluded from conf: match restricted by cluster",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Cluster: "test",
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 0,
		},
		{
			name: "Resources excluded from conf: match restricted by cluster (regexp)",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Cluster: "t.*",
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 0,
		},
		{
			name: "Resources excluded from conf: match restricted by apiVersion",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						TypeMeta: v1.TypeMeta{APIVersion: "velero.io/v1"},
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 1,
		},
		{
			name: "Resources excluded from conf: match restricted by apiVersion and kind",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						TypeMeta: v1.TypeMeta{APIVersion: "velero.io/v1", Kind: "Backup"},
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by namespace",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Namespace: "velero",
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 1,
		},
		{
			name: "Resources excluded from conf: match restricted by namespace (regexp)",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Namespace: "v.*",
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 0,
		},
		{
			name: "Resources excluded from conf: match restricted by annotation",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Annotations: map[string]string{"test-annotation": "velero-capi-backup-1"},
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by annotation (regexp)",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Annotations: map[string]string{"test-annotation": "v.*"},
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by label",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Labels: map[string]string{"test-label": "velero-capi-backup-2"},
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by label (regexp)",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Labels: map[string]string{"test-label": "v.*"},
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by name",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Name: "velero-capi-backup-1",
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 2,
		},
		{
			name: "Resources excluded from conf: match restricted by name (regexp)",
			filters: func(t *testing.T) []FilterFunc {
				rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
					{
						Name: "velero-capi-backup-(1|2)",
					},
				})
				require.NoError(t, err)

				return []FilterFunc{IgnoreRuleExclusions("test", rules)}
			},
			list:         getExclusionListResourceSet,
			expectedPass: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ch := make(chan unstructured.Unstructured, test.expectedPass+1)
			discovery := NewDiscovery(klog.NewKlogr(), test.filters(t)...)
			err := discovery.Discover(t.Context(), test.list(), ch)
			require.NoError(t, err)
			assert.Equal(t, test.expectedPass, len(ch))
		})
	}
}

func TestCompileExclusions(t *testing.T) {
	t.Run("alternation cannot escape the implicit anchors", func(t *testing.T) {
		rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
			{Name: "velero-.*|backup"},
		})
		require.NoError(t, err)
		require.Len(t, rules, 1)

		filter := IgnoreRuleExclusions("test", rules)

		newResource := func(name string) unstructured.Unstructured {
			res := unstructured.Unstructured{}
			res.SetName(name)
			return res
		}

		assert.Assert(t, !filter(newResource("backup-old"), klog.NewKlogr()))
		assert.Assert(t, filter(newResource("velero-x"), klog.NewKlogr()))
		assert.Assert(t, filter(newResource("backup"), klog.NewKlogr()))
	})

	t.Run("invalid expressions are reported at compile time with rule index, field and expression", func(t *testing.T) {
		rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
			{Name: "("},
		})
		require.Nil(t, rules)
		require.Error(t, err)

		var ruleErrs ExclusionRuleErrors
		require.ErrorAs(t, err, &ruleErrs)
		require.Len(t, ruleErrs, 1)
		assert.Equal(t, 1, ruleErrs[0].RuleIndex)
		assert.Equal(t, "name", ruleErrs[0].Field)
		assert.Equal(t, "(", ruleErrs[0].Expression)
		require.Error(t, ruleErrs[0].Err)

		assert.Assert(t, strings.Contains(err.Error(), `excludeResources[1].name`))
		assert.Assert(t, strings.Contains(err.Error(), `"("`))
	})

	t.Run("three rules with one problem each yield three errors in rule order", func(t *testing.T) {
		rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
			{Name: "("},
			{Namespace: "["},
			{Cluster: "*"},
		})
		require.Nil(t, rules)
		require.Error(t, err)

		var ruleErrs ExclusionRuleErrors
		require.ErrorAs(t, err, &ruleErrs)
		require.Len(t, ruleErrs, 3)

		require.Equal(t, []int{1, 2, 3}, []int{
			ruleErrs[0].RuleIndex,
			ruleErrs[1].RuleIndex,
			ruleErrs[2].RuleIndex,
		})
		assert.Equal(t, "name", ruleErrs[0].Field)
		assert.Equal(t, "namespace", ruleErrs[1].Field)
		assert.Equal(t, "cluster", ruleErrs[2].Field)
		assert.Equal(t, "(", ruleErrs[0].Expression)
		assert.Equal(t, "[", ruleErrs[1].Expression)
		assert.Equal(t, "*", ruleErrs[2].Expression)
	})

	t.Run("a single rule may report several problems", func(t *testing.T) {
		rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
			{Annotations: map[string]string{"b": "*", "a": "("}},
		})
		require.Nil(t, rules)
		require.Error(t, err)

		var ruleErrs ExclusionRuleErrors
		require.ErrorAs(t, err, &ruleErrs)
		require.Len(t, ruleErrs, 2)

		// Keys are reported in sorted order so the output is stable.
		assert.Equal(t, "annotations[a]", ruleErrs[0].Field)
		assert.Equal(t, "annotations[b]", ruleErrs[1].Field)
	})

	t.Run("empty rules are rejected with a hint for an explicit match-all", func(t *testing.T) {
		rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
			{},
		})
		require.Nil(t, rules)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrEmptyExclusionRule)
		assert.Assert(t, strings.Contains(err.Error(), `name: ".*"`))
		require.ErrorContains(t, err, "excludeResources[1]")
	})

	t.Run("explicit match-all is accepted and matches every resource", func(t *testing.T) {
		rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
			{Name: ".*"},
		})
		require.NoError(t, err)
		require.Len(t, rules, 1)

		filter := IgnoreRuleExclusions("test", rules)

		res := unstructured.Unstructured{}
		res.SetName("anything")
		res.SetNamespace("anywhere")
		assert.Assert(t, filter(res, klog.NewKlogr()))
	})

	t.Run("apiVersion and kind are compared literally", func(t *testing.T) {
		rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
			{
				TypeMeta: v1.TypeMeta{APIVersion: "velero.io/v1", Kind: "Backup"},
			},
		})
		require.NoError(t, err)

		filter := IgnoreRuleExclusions("test", rules)

		matching := unstructured.Unstructured{}
		matching.SetAPIVersion("velero.io/v1")
		matching.SetKind("Backup")
		assert.Assert(t, filter(matching, klog.NewKlogr()))

		// Same string as a pattern would match, but apiVersion/kind are not patterns.
		otherVersion := unstructured.Unstructured{}
		otherVersion.SetAPIVersion("velero.io/v2")
		otherVersion.SetKind("Backup")
		assert.Assert(t, !filter(otherVersion, klog.NewKlogr()))

		otherKind := unstructured.Unstructured{}
		otherKind.SetAPIVersion("velero.io/v1")
		otherKind.SetKind("Backuped")
		assert.Assert(t, !filter(otherKind, klog.NewKlogr()))
	})

	t.Run("empty metadata expressions match only an empty value", func(t *testing.T) {
		rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
			{Labels: map[string]string{"present": ""}},
		})
		require.NoError(t, err)

		filter := IgnoreRuleExclusions("test", rules)

		emptyValue := unstructured.Unstructured{}
		emptyValue.SetLabels(map[string]string{"present": ""})
		assert.Assert(t, filter(emptyValue, klog.NewKlogr()))

		withValue := unstructured.Unstructured{}
		withValue.SetLabels(map[string]string{"present": "any"})
		assert.Assert(t, !filter(withValue, klog.NewKlogr()))

		withoutLabel := unstructured.Unstructured{}
		withoutLabel.SetLabels(map[string]string{"other": "any"})
		assert.Assert(t, !filter(withoutLabel, klog.NewKlogr()))
	})
}

func TestCompileExclusionsLogsNothing(t *testing.T) {
	var buf bytes.Buffer

	klog.LogToStderr(false)
	klog.SetOutput(&buf)
	t.Cleanup(func() {
		klog.LogToStderr(true)
		klog.SetOutput(os.Stderr)
	})

	rules, err := CompileExclusions([]gitopszombiesv1.ExcludeResources{
		{Name: "("},
	})
	require.Error(t, err)
	require.Nil(t, rules)

	require.Empty(t, buf.String())
}
