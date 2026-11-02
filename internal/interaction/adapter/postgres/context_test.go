package postgres

import "testing"

func TestRecentContextTurnLimitUsesServerOwnedConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  int
	}{
		{name: "configured", value: 24, want: 24},
		{name: "safe default", value: 0, want: defaultRecentContextTurnLimit},
		{name: "negative defaults", value: -1, want: defaultRecentContextTurnLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := (Store{MaxRecentTurns: test.value}).recentContextTurnLimit(); got != test.want {
				t.Fatalf("recent ContextPack turn limit = %d, want %d", got, test.want)
			}
		})
	}
}
