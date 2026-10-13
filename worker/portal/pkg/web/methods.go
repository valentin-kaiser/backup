package web

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/audit"
)

// prefixes are the spellings of the service the control accepts in front of a method name.
var prefixes = []string{"Control.", "Control/", "backup.api.v1.Control.", "backup.api.v1.Control/"}

// reads are the methods of the control API that only look at things. They are not recorded in the audit log.
var reads = map[string]bool{
	"GetStatus": true, "ListRuns": true, "GetRun": true, "ListSnapshots": true, "ListFiles": true, "GetStats": true,
	"Watch": true, "ListSecrets": true, "ListPeers": true, "GetSyncServe": true, "TestPeer": true,
}

// field names a parameter of a call by its protobuf field number, which is how the parameters travel.
type field struct{ num, label string }

// spec says how a call that changes something is recorded: under which action, and which of its parameters are
// written down. Only parameters that are safe to keep are listed, never a password, a secret value or the arguments of
// restic, which may contain credentials. The first field is the target of the entry.
type spec struct {
	action string
	fields []field
}

var changes = map[string]spec{
	"StartJob":  {"start_job", []field{{"1", "job"}}},
	"CancelJob": {"cancel_job", []field{{"1", "job"}}},
	"UnlockJob": {"unlock_job", []field{{"1", "job"}}},
	"PauseJob":  {"pause_job", []field{{"1", "job"}}},
	"ResumeJob": {"resume_job", []field{{"1", "job"}}},
	"DeleteJob": {"delete_job", []field{{"1", "job"}}},
	"CreateJob": {"create_job", jobFields},
	"UpdateJob": {"update_job", jobFields},
	// RestoreRequest: snapshot 1, target 2, include 3, job 4.
	"Restore": {"restore", []field{{"4", "job"}, {"1", "snapshot"}, {"2", "to"}, {"3", "paths"}}},
	// The value (field 2) of a secret is never read. Field 3 only says whether it was generated.
	"SetSecret":                {"set_secret", []field{{"1", "secret"}, {"3", "generated"}}},
	"ChangeRepositoryPassword": {"change_repository_password", []field{{"1", "secret"}, {"3", "generated"}}},
	"DeleteSecret":             {"delete_secret", []field{{"1", "secret"}}},
	"ResyncJob":                {"resync_job", []field{{"1", "job"}}},
	"DeletePeer":               {"delete_peer", []field{{"1", "peer"}}},
	// Peer: name 1, url 2, username 3, password secret 5, api url 7, api password secret 9. Password files are left out.
	"SetPeer": {"set_peer", []field{{"1", "peer"}, {"2", "url"}, {"3", "username"}, {"5", "password secret"}, {"7", "api url"}, {"9", "api password secret"}}},
	// SyncServe: enabled 1, path 2, address 3, port 4, username 5, password secret 7.
	"SetSyncServe": {"set_sync_serve", []field{{"1", "enabled"}, {"2", "path"}, {"3", "address"}, {"4", "port"}, {"5", "username"}, {"7", "password secret"}, {"10", "tunnel"}}},
}

// jobFields are the fields of a JobDefinition worth recording: name 1, type 2, enabled 3, schedule 4, source 8,
// repository 9 (credentials in its address are removed), password file 10, worker 11, password secret 12. The arguments
// (5) are left out, they can hold credentials.
var jobFields = []field{{"1", "name"}, {"2", "type"}, {"3", "enabled"}, {"4", "schedule"}, {"8", "source"},
	{"9", "repository"}, {"10", "password file"}, {"11", "worker"}, {"12", "password secret"}, {"13", "path1"}, {"14", "path2"},
	{"15", "conflict"}, {"16", "conflict loser"}, {"17", "max delete pct"}, {"18", "check access"}, {"19", "owner"}, {"20", "force"}, {"21", "engine"}}

// Known reports whether the method belongs to the control API. The exact spelling is required: anything else the
// daemon might resolve differently is refused. The cancel and close notifications end a stream the caller started.
func known(method string) (name string, ok bool) {
	if method == "xrpc.cancel" || method == "xrpc.close" {
		return method, true
	}
	for _, p := range prefixes {
		if n, found := strings.CutPrefix(method, p); found {
			if reads[n] {
				return n, true
			}
			if _, changes := changes[n]; changes {
				return n, true
			}
			return "", false
		}
	}
	return "", false
}

// text renders a JSON value of a parameter as a short string.
func text(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return fmt.Sprint(x)
	case float64:
		return fmt.Sprint(x)
	case []any:
		return fmt.Sprintf("%d", len(x))
	}
	return ""
}

// describe builds the entry fields for a call: the target and a short description of the parameters.
func (s spec) describe(params json.RawMessage) (target, detail string) {
	var p map[string]json.RawMessage
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	var parts []string
	for i, f := range s.fields {
		v := audit.Redact(text(p[f.num]))
		if i == 0 {
			target = v
			continue
		}
		if v != "" && v != "false" && v != "0" {
			parts = append(parts, f.label+"="+v)
		}
	}
	sort.Strings(parts)
	return target, strings.Join(parts, ", ")
}
