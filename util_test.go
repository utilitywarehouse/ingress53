package main

import (
	"reflect"
	"testing"
)

func TestDiffStringSlices(t *testing.T) {
	testCases := []struct {
		A []string
		B []string
		R []string
	}{
		{
			A: []string{"A"},
			B: []string{"A"},
			R: []string{},
		},
		{
			A: []string{"A"},
			B: []string{"B"},
			R: []string{"A"},
		},
		{
			A: []string{},
			B: []string{"B"},
			R: []string{},
		},
		{
			A: []string{"A"},
			B: []string{},
			R: []string{"A"},
		},
		{
			A: []string{"A", "B", "C"},
			B: []string{"B", "D", "A"},
			R: []string{"C"},
		},
	}

	for i, test := range testCases {
		r := diffStringSlices(test.A, test.B)
		if !reflect.DeepEqual(r, test.R) {
			t.Errorf("diffStringSlices returned unexpected result for test case #%02d: %+v", i, r)
		}
	}

}
