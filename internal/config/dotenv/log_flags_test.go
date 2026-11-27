package dotenv

import (
	flag "github.com/spf13/pflag"
	"testing"
)

func TestEpisodeLogBindings(t *testing.T) {
	for _, tc := range []struct {
		value    string
		disabled bool
	}{{"1", false}, {"true", false}, {"0", true}, {"false", true}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("MEMEVAL_EPISODE_LOGS", tc.value)
			t.Setenv("MEMEVAL_LOG_FILE_CAP_BYTES", "1024")
			f := flag.NewFlagSet("fixture", flag.ContinueOnError)
			disabled := f.Bool("no-logs", false, "")
			cap := f.Int64("log-file-cap", 33554432, "")
			if e := ApplyFlags(f, nil); e != nil || *disabled != tc.disabled || *cap != 1024 {
				t.Fatal(e, *disabled, *cap)
			}
			args := []string{"--no-logs=false", "--log-file-cap=2048"}
			if e := ApplyFlags(f, args); e != nil {
				t.Fatal(e)
			}
			if e := f.Parse(args); e != nil {
				t.Fatal(e)
			}
			if *disabled || *cap != 2048 {
				t.Fatal("explicit flags did not override environment")
			}
		})
	}
	t.Setenv("MEMEVAL_EPISODE_LOGS", "private-invalid-value")
	f := flag.NewFlagSet("fixture", flag.ContinueOnError)
	f.Bool("no-logs", false, "")
	if e := ApplyFlags(f, nil); e == nil || e.Error() != "invalid configuration for MEMEVAL_EPISODE_LOGS" {
		t.Fatal(e)
	}
	for _, b := range Bindings {
		if b.Flag == "no-logs" && Default(b) != "false" {
			t.Fatal("default logging must be enabled")
		}
	}
}
