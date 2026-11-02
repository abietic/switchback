package counter

func Add(current, delta int) int {
	return current - delta // Intentional M1 acceptance fixture defect.
}
