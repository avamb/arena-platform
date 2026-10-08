package eventbot

// wizard_regions.go — the country question groups ~100 countries by region
// (migration 0127, countries.region): a person first picks "Europe" or "Latin
// America" and only then sees that region's countries, instead of one list of
// a hundred buttons.

// regionOrder is the order the region buttons appear in. A region with no
// countries is not offered.
var regionOrder = []string{"europe", "north_america", "latin_america", "middle_east", "asia", "africa", "oceania", "other"}

// regionListMin is the size up to which the country list stays flat: a short
// list (a young installation, tests) needs no extra step.
const regionListMin = 12

// regionKey is the bot.wz.region.* text key of a region.
func regionKey(region string) string { return "bot.wz.region." + region }

// regionOf is the region of a country as the API reported it, "other" when it
// said nothing.
func regionOf(c RefItem) string {
	for _, r := range regionOrder {
		if c.Region == r {
			return r
		}
	}
	return "other"
}

// presentRegions returns the regions that hold at least one of the countries,
// in regionOrder.
func presentRegions(countries []RefItem) []string {
	have := map[string]bool{}
	for _, c := range countries {
		have[regionOf(c)] = true
	}
	out := make([]string, 0, len(regionOrder))
	for _, r := range regionOrder {
		if have[r] {
			out = append(out, r)
		}
	}
	return out
}

// groupByRegion reports whether the country question should go through
// regions first.
func groupByRegion(countries []RefItem) bool {
	return len(countries) > regionListMin && len(presentRegions(countries)) > 1
}

// inRegion keeps the countries of one region.
func inRegion(countries []RefItem, region string) []RefItem {
	out := make([]RefItem, 0, len(countries))
	for _, c := range countries {
		if regionOf(c) == region {
			out = append(out, c)
		}
	}
	return out
}

// validRegion reports whether region is one the countries actually have.
func validRegion(countries []RefItem, region string) bool {
	for _, r := range presentRegions(countries) {
		if r == region {
			return true
		}
	}
	return false
}
