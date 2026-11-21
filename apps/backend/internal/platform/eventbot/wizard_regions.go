package eventbot

// wizard_regions.go — the country question groups the countries of the world
// by region (the seven regions of the server's world table): a person first
// picks "Europe" or "Latin America" and only then sees that region's countries
// (country_choice.go), instead of one list of 250 buttons.

// regionKey is the bot.wz.region.* text key of a region.
func regionKey(region string) string { return "bot.wz.region." + region }
