package services

import "testing"

func TestChannelLaunchRequiresWebForOriginalsAndExternalAgents(t *testing.T) {
	tests := []struct {
		name     string
		info     serviceAdmission
		external []string
		webOnly  bool
	}{
		{name: "text"},
		{name: "optional supplement", info: serviceAdmission{calculation: true}},
		{name: "citation source", info: serviceAdmission{needsSource: true}, webOnly: true},
		{name: "csv choice", info: serviceAdmission{csvChoice: true}, webOnly: true},
		{name: "picture", info: serviceAdmission{usesPictures: true}, webOnly: true},
		{name: "remote agent", external: []string{"Partner"}, webOnly: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := channelWebOnly(test.info, test.external); (got != "") != test.webOnly {
				t.Fatalf("web-only reason = %q, want required = %t", got, test.webOnly)
			}
		})
	}
}
