package assert

func That(condition bool, message string) {
	if !condition {
		panic("assertion failed: " + message)
	}
}
