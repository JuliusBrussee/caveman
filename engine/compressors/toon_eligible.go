package compressors

// tabularEligibility reports what fraction of array elements sit inside
// uniform flat-object arrays, plus whether the top-level value is one.
func tabularEligibility(v any) (fraction float64, topLevelTabular bool) {
	tv, ok := asTOONValue(v)
	if !ok {
		return 0, false
	}
	eligible, total := countTabularEligibility(tv)
	_, _, topLevelTabular = tabularRows(tv.arr, ',')
	if total == 0 {
		return 0, topLevelTabular
	}
	return float64(eligible) / float64(total), topLevelTabular
}

func countTabularEligibility(v toonValue) (eligible, total int) {
	switch v.kind {
	case toonArray:
		total += len(v.arr)
		if _, _, ok := tabularRows(v.arr, ','); ok {
			eligible += len(v.arr)
		}
		for _, e := range v.arr {
			eEligible, eTotal := countTabularEligibility(e)
			eligible += eEligible
			total += eTotal
		}
	case toonObject:
		for _, f := range v.obj {
			fEligible, fTotal := countTabularEligibility(f.value)
			eligible += fEligible
			total += fTotal
		}
	}
	return eligible, total
}
