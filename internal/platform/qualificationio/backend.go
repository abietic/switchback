package qualificationio

func backendMask(testCase string) uint64 {
	switch testCase {
	case "without-chown":
		return 0xa
	case "without-dac-override":
		return 0x9
	case "without-fowner":
		return 0x3
	}
	return 0xb
}
func backendMode(testCase string) uint32 {
	switch positiveCase(testCase) {
	case "private", "worker-handoff":
		return 0700
	case "sticky":
		return 01777
	}
	return 0755
}
func backendOperation(testCase string) string {
	switch positiveCase(testCase) {
	case "private":
		return "unlink_private_child"
	case "sticky":
		return "unlink_sticky_child"
	case "worker-handoff":
		return "worker_handoff"
	}
	return "chown"
}

func backendCleanupEntries(testCase string) uint64 {
	if testCase == "nonempty" || testCase == "worker-handoff" {
		return 1
	}
	return 0
}
