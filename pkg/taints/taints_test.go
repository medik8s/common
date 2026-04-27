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
		name         string
		args         args
		expectedErr  bool
		expectedInfo OutOfServiceTaintInfo
	}{
		//valid use-cases
		{name: "validEnabledNoPlus", args: args{&version.Info{Major: "1", Minor: "26"}}, expectedErr: false, expectedInfo: OutOfServiceTaintInfo{Supported: true, GA: false}},
		{name: "validDisabledEnabledNoPlus", args: args{&version.Info{Major: "1", Minor: "24"}}, expectedErr: false, expectedInfo: OutOfServiceTaintInfo{Supported: false, GA: false}},
		{name: "validEnabledWithPlus", args: args{&version.Info{Major: "1", Minor: "26+"}}, expectedErr: false, expectedInfo: OutOfServiceTaintInfo{Supported: true, GA: false}},
		{name: "validDisabledWithPlus", args: args{&version.Info{Major: "1", Minor: "24+"}}, expectedErr: false, expectedInfo: OutOfServiceTaintInfo{Supported: false, GA: false}},
		{name: "validEnabledWithTrailingChars", args: args{&version.Info{Major: "1", Minor: "26.5.2#$%+"}}, expectedErr: false, expectedInfo: OutOfServiceTaintInfo{Supported: true, GA: false}},
		{name: "validDisabledWithTrailingChars", args: args{&version.Info{Major: "1", Minor: "22.5.2#$%+"}}, expectedErr: false, expectedInfo: OutOfServiceTaintInfo{Supported: false, GA: false}},
		{name: "validGANoPlus", args: args{&version.Info{Major: "1", Minor: "28"}}, expectedErr: false, expectedInfo: OutOfServiceTaintInfo{Supported: true, GA: true}},
		{name: "validGAWithPlus", args: args{&version.Info{Major: "1", Minor: "28+"}}, expectedErr: false, expectedInfo: OutOfServiceTaintInfo{Supported: true, GA: true}},
		{name: "validGAWithTrailingChars", args: args{&version.Info{Major: "1", Minor: "29.5.2#$%+"}}, expectedErr: false, expectedInfo: OutOfServiceTaintInfo{Supported: true, GA: true}},

		//invalid use-cases
		{name: "inValidNoPlus", args: args{&version.Info{Major: "1", Minor: "%24"}}, expectedErr: true, expectedInfo: OutOfServiceTaintInfo{Supported: false, GA: false}},
		{name: "inValidWithPlus", args: args{&version.Info{Major: "1+", Minor: "26"}}, expectedErr: true, expectedInfo: OutOfServiceTaintInfo{Supported: false, GA: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taintInfo = OutOfServiceTaintInfo{}
			if err := setOutOfTaintFlags(tt.args.version); (err != nil) != tt.expectedErr || taintInfo != tt.expectedInfo {
				t.Errorf("setOutOfTaintFlags() error = %v, expectedErr %v, expected %+v, got %+v", err, tt.expectedErr, tt.expectedInfo, taintInfo)
			}
		})
	}
}
