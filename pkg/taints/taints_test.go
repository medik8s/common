package taints

import (
	"testing"

	"k8s.io/apimachinery/pkg/version"
)

func Test_setOutOfTaintFlags(t *testing.T) {
	type args struct {
		version *version.Info
	}
	tests := []struct {
		name                    string
		args                    args
		wantErr                 bool
		isOutOfTaintFlagEnabled bool
		isOutOfTaintGA          bool
	}{
		//valid use-cases
		{name: "validEnabledNoPlus", args: args{&version.Info{Major: "1", Minor: "26"}}, wantErr: false, isOutOfTaintFlagEnabled: true, isOutOfTaintGA: false},
		{name: "validDisabledEnabledNoPlus", args: args{&version.Info{Major: "1", Minor: "24"}}, wantErr: false, isOutOfTaintFlagEnabled: false, isOutOfTaintGA: false},
		{name: "validEnabledWithPlus", args: args{&version.Info{Major: "1", Minor: "26+"}}, wantErr: false, isOutOfTaintFlagEnabled: true, isOutOfTaintGA: false},
		{name: "validDisabledWithPlus", args: args{&version.Info{Major: "1", Minor: "24+"}}, wantErr: false, isOutOfTaintFlagEnabled: false, isOutOfTaintGA: false},
		{name: "validEnabledWithTrailingChars", args: args{&version.Info{Major: "1", Minor: "26.5.2#$%+"}}, wantErr: false, isOutOfTaintFlagEnabled: true, isOutOfTaintGA: false},
		{name: "validDisabledWithTrailingChars", args: args{&version.Info{Major: "1", Minor: "22.5.2#$%+"}}, wantErr: false, isOutOfTaintFlagEnabled: false, isOutOfTaintGA: false},
		{name: "validGANoPlus", args: args{&version.Info{Major: "1", Minor: "28"}}, wantErr: false, isOutOfTaintFlagEnabled: true, isOutOfTaintGA: true},
		{name: "validGAWithPlus", args: args{&version.Info{Major: "1", Minor: "28+"}}, wantErr: false, isOutOfTaintFlagEnabled: true, isOutOfTaintGA: true},
		{name: "validGAWithTrailingChars", args: args{&version.Info{Major: "1", Minor: "29.5.2#$%+"}}, wantErr: false, isOutOfTaintFlagEnabled: true, isOutOfTaintGA: true},

		//invalid use-cases
		{name: "inValidNoPlus", args: args{&version.Info{Major: "1", Minor: "%24"}}, wantErr: true, isOutOfTaintFlagEnabled: false, isOutOfTaintGA: false},
		{name: "inValidWithPlus", args: args{&version.Info{Major: "1+", Minor: "26"}}, wantErr: true, isOutOfTaintFlagEnabled: false, isOutOfTaintGA: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			IsOutOfServiceTaintSupported = false
			IsOutOfServiceTaintGA = false
			if err := setOutOfTaintFlags(tt.args.version); (err != nil) != tt.wantErr || IsOutOfServiceTaintSupported != tt.isOutOfTaintFlagEnabled || IsOutOfServiceTaintGA != tt.isOutOfTaintGA {
				t.Errorf("setOutOfTaintFlags() error = %v, wantErr %v, expected out of taint flag supported %v, got %v, expected GA %v, got %v", err, tt.wantErr, tt.isOutOfTaintFlagEnabled, IsOutOfServiceTaintSupported, tt.isOutOfTaintGA, IsOutOfServiceTaintGA)
			}
		})
	}
}
