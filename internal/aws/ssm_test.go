package aws

import "testing"

func TestParseRunnerProcesses(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		listeners int
		workers   int
	}{
		{"nothing running", "", 0, 0},
		{
			"two idle runners, non-default install layout",
			"github2  1314626  0.0  1.8 274168320 74324 ?     Sl   Sep11   2:59 /home/github2/bin/Runner.Listener run\n" +
				"github   1314634  0.0  1.7 274168344 69580 ?     Sl   Sep11   2:58 /home/github/actions-runner/bin/Runner.Listener run\n",
			2, 0,
		},
		{
			"one of two runners executing a job",
			"github-+ 1201  0.1  1.9 274168320 75000 ?  Sl 02:31 0:03 /opt/actions-runner-1/bin/Runner.Listener run\n" +
				"github-+ 1202  0.1  1.9 274168320 75000 ?  Sl 02:31 0:03 /opt/actions-runner-2/bin/Runner.Listener run\n" +
				"github-+ 5310 12.0  4.2 274300000 160000 ? Sl 03:01 0:40 /opt/actions-runner-2/bin/Runner.Worker spawnclient 125 128\n",
			2, 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ParseRunnerProcesses(tt.output)
			if p.Listeners != tt.listeners || p.Workers != tt.workers || p.Busy() != (tt.workers > 0) {
				t.Errorf("ParseRunnerProcesses() = %d listeners, %d workers, busy=%v; want %d, %d", p.Listeners, p.Workers, p.Busy(), tt.listeners, tt.workers)
			}
		})
	}
}
