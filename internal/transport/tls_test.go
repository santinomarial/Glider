package transport

import "testing"

func TestAuthorizationIsDenyByDefault(t *testing.T) {
	cases := []struct {
		role, method string
		want         bool
	}{{"viewer", "/glider.v1.ControlPlane/ListTasks", true}, {"viewer", "/glider.v1.ControlPlane/PutTask", false}, {"operator", "/glider.v1.ControlPlane/PutWorkload", true}, {"operator", "/glider.v1.ControlPlane/PutNode", false}, {"node", "/glider.v1.ControlPlane/PutNode", true}, {"node", "/glider.v1.ControlPlane/ListTasks", false}, {"unknown", "/glider.v1.ControlPlane/ListTasks", false}, {"admin", "/anything/DeleteEverything", true}}
	for _, tc := range cases {
		if got := authorized(map[string]bool{tc.role: true}, tc.method); got != tc.want {
			t.Errorf("%s %s=%v want %v", tc.role, tc.method, got, tc.want)
		}
	}
}
