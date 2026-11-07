package directions

import "testing"

func f(v float64) *float64 { return &v }

func TestBuild(t *testing.T) {
	cases := []struct {
		name         string
		lat, lng     *float64
		addr, city   string
		google, waze string
	}{
		{"coords", f(32.0853), f(34.7818), "x", "y",
			"https://www.google.com/maps/search/?api=1&query=32.0853,34.7818",
			"https://waze.com/ul?ll=32.0853,34.7818&navigate=yes"},
		{"six decimals rounded", f(50.123456789), f(-14.1), "", "",
			"https://www.google.com/maps/search/?api=1&query=50.123457,-14.1",
			"https://waze.com/ul?ll=50.123457,-14.1&navigate=yes"},
		{"integer coords", f(32), f(34), "", "",
			"https://www.google.com/maps/search/?api=1&query=32,34",
			"https://waze.com/ul?ll=32,34&navigate=yes"},
		{"lng missing falls back to address", f(32.1), nil, "Dizengoff 5", "Tel Aviv",
			"https://www.google.com/maps/search/?api=1&query=Dizengoff+5%2C+Tel+Aviv",
			"https://waze.com/ul?q=Dizengoff+5%2C+Tel+Aviv&navigate=yes"},
		{"address only", nil, nil, " Rothschild 1 ", "",
			"https://www.google.com/maps/search/?api=1&query=Rothschild+1",
			"https://waze.com/ul?q=Rothschild+1&navigate=yes"},
		{"out of range falls back", f(91), f(10), "A", "B",
			"https://www.google.com/maps/search/?api=1&query=A%2C+B",
			"https://waze.com/ul?q=A%2C+B&navigate=yes"},
		{"nothing", nil, nil, "  ", "City", "", ""},
		{"cyrillic", nil, nil, "Пушкина 1", "Москва",
			"https://www.google.com/maps/search/?api=1&query=%D0%9F%D1%83%D1%88%D0%BA%D0%B8%D0%BD%D0%B0+1%2C+%D0%9C%D0%BE%D1%81%D0%BA%D0%B2%D0%B0",
			"https://waze.com/ul?q=%D0%9F%D1%83%D1%88%D0%BA%D0%B8%D0%BD%D0%B0+1%2C+%D0%9C%D0%BE%D1%81%D0%BA%D0%B2%D0%B0&navigate=yes"},
	}
	for _, c := range cases {
		got := Build(c.lat, c.lng, c.addr, c.city)
		if got.GoogleURL != c.google || got.WazeURL != c.waze {
			t.Errorf("%s: got %+v", c.name, got)
		}
		if got.Empty() != (c.google == "") {
			t.Errorf("%s: Empty() wrong", c.name)
		}
	}
}
