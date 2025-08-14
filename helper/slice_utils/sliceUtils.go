package slice_utils

func Filter[T any](s []T, p func(i T) bool) []T {
	var result []T
	for _, i := range s {
		if p(i) {
			result = append(result, i)
		}
	}
	return result
}
