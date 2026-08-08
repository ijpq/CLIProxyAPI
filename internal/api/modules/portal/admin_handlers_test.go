package portal

import (
	"reflect"
	"testing"
)

func TestRejectedSelections(t *testing.T) {
	tests := []struct {
		name      string
		requested []string
		accepted  []string
		want      []string
	}{
		{name: "all accepted", requested: []string{"model-a", "model-b"}, accepted: []string{"model-a", "model-b"}},
		{name: "reports unknown without duplicate blanks", requested: []string{"model-a", " missing ", "missing", ""}, accepted: []string{"model-a"}, want: []string{"missing"}},
		{name: "empty clears restriction", requested: nil, accepted: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rejectedSelections(tt.requested, tt.accepted); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rejectedSelections() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
