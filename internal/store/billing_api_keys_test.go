package store

import (
	"strings"
	"testing"
)

func TestAPIKeyPoliciesDoNotConfuseEmptyWithAll(t *testing.T) {
	for _, policy := range []APIKeyPolicy{
		{ModelScope: "selected"},
		{ModelScope: "none", AllowedModels: []string{"model-a"}},
		{ModelScope: "all", AllowedModels: []string{"model-a"}},
		{ModelScope: "unknown"},
		{AllowedModels: []string{"model-a"}},
		{ModelScope: "selected", AllowedModels: []string{"model-a,model-b"}},
		{ModelScope: "selected", AllowedModels: []string{"model\x00-a"}},
		{ModelScope: "selected", AllowedModels: []string{strings.Repeat("a", 257)}},
	} {
		if _, err := NormalizeKeyPolicy(policy); err != ErrInvalidKeyPolicy {
			t.Fatalf("accepted invalid policy %#v: %v", policy, err)
		}
	}
	policy, err := NormalizeKeyPolicy(APIKeyPolicy{ModelScope: "selected", AllowedModels: []string{" Model-A(high) ", "MODEL-A"}})
	if err != nil || len(policy.AllowedModels) != 1 || policy.AllowedModels[0] != "model-a" {
		t.Fatalf("normalization: %#v %v", policy, err)
	}
	for _, scope := range []string{"none", "unknown"} {
		if models := effectiveKeyModels(nil, APIKeyPolicy{ModelScope: scope}); models == nil || len(models) != 0 {
			t.Fatal("denial became all", models)
		}
	}
	if models := effectiveKeyModels([]string{"account-model"}, policy); models == nil || len(models) != 0 {
		t.Fatal("disjoint permissions became unrestricted", models)
	}
}
