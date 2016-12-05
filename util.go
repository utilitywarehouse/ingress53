package main

func diffStringSlices(a []string, b []string) []string {
	ret := []string{}

	for _, va := range a {
		exists := false
		for _, vb := range b {
			if va == vb {
				exists = true
				break
			}
		}

		if !exists {
			ret = append(ret, va)
		}
	}

	return ret
}
