package lark

import "testing"

func TestRunDestination(t *testing.T) {
	for _, tc := range []struct {
		name string
		ext  map[string]string
		want feishuTarget
	}{
		{name: "private chat"},
		{name: "chat without a thread", ext: map[string]string{"chat": "oc_topic"}},
		{name: "thread without a chat", ext: map[string]string{"thread": "omt_topic"}},
		{
			name: "topic",
			ext:  map[string]string{"chat": "oc_topic", "thread": "omt_topic"},
			want: feishuTarget{ChatID: "oc_topic", InThread: true},
		},
		{
			name: "group read takes precedence over topic",
			ext:  map[string]string{"read_chat": "oc_read", "chat": "oc_topic", "thread": "omt_topic"},
			want: feishuTarget{ChatID: "oc_read", NoDetour: true, Read: true},
		},
		{
			name: "read in a thread",
			ext:  map[string]string{"read_chat": "oc_read", "read_thread": "omt_read"},
			want: feishuTarget{ChatID: "oc_read", InThread: true, NoDetour: true, Read: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runTo(tc.ext); got != tc.want {
				t.Errorf("destination %+v, want %+v", got, tc.want)
			}
		})
	}
}
