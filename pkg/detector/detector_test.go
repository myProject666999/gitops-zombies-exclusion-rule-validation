package detector

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	gitopszombiesv1 "github.com/raffis/gitops-zombies/pkg/apis/gitopszombies/v1"
	"github.com/raffis/gitops-zombies/pkg/collector"
)

func TestNewRejectsInvalidExclusionsBeforeContactingACluster(t *testing.T) {
	conf := &gitopszombiesv1.Config{
		ExcludeResources: []gitopszombiesv1.ExcludeResources{
			{Name: "("},
		},
	}

	// No kubeconfig is configured, so any error about the cluster would come from getDynClient. The exclusion
	// rules have to be validated first.
	detector, err := New(conf, genericclioptions.NewConfigFlags(false), nil)
	require.Nil(t, detector)
	require.Error(t, err)

	var ruleErrs collector.ExclusionRuleErrors
	require.ErrorAs(t, err, &ruleErrs)
	require.Len(t, ruleErrs, 1)
}
