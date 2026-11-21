package eventbot

// seatLayoutKeys lists every bot.sl.* key of the seat layout screens (task
// S3c of spec 40) and layoutAlgorithmKeys the labels of the allocator's own
// algorithms and options (layoutgen.*): arena-api names them by key
// (label_key, hint_key), the bot owns the words. The locale test checks all of
// them in every supported catalog together with MessageKeys; an algorithm
// added later whose keys no catalog has is shown by its machine name.
var seatLayoutKeys = []string{
	"bot.sl.btn", "bot.sl.scheme_btn", "bot.sl.head", "bot.sl.not_seated", "bot.sl.need_one", "bot.sl.intro",
	"bot.sl.cat_line", "bot.sl.closed_note", "bot.sl.no_algorithms", "bot.sl.last_batch", "bot.sl.undo_btn",
	"bot.sl.undo_hint", "bot.sl.algorithm_gone", "bot.sl.gone", "bot.sl.q_stage", "bot.sl.stage_top",
	"bot.sl.stage_bottom", "bot.sl.stage_left", "bot.sl.stage_right", "bot.sl.show_btn", "bot.sl.options_btn",
	"bot.sl.err_stage", "bot.sl.err_param", "bot.sl.err_plan", "bot.sl.err_fit", "bot.sl.err_too_big",
	"bot.sl.err_apply", "bot.sl.preview_title", "bot.sl.preview_line", "bot.sl.preview_frozen",
	"bot.sl.preview_closed", "bot.sl.preview_estimate", "bot.sl.preview_nothing", "bot.sl.preview_hint",
	"bot.sl.apply_btn", "bot.sl.out_of_date", "bot.sl.stale", "bot.sl.applied", "bot.sl.applied_skipped",
	"bot.sl.undone", "bot.sl.undone_skipped", "bot.sl.err_undo_nothing", "bot.sl.err_undo_done",
	"bot.sl.err_undo_gone",
	// A date with one category is cut into new ones (spec 64 section 2.4).
	"bot.sl.intro_one", "bot.sl.q_cats", "bot.sl.q_top", "bot.sl.q_bottom", "bot.sl.err_price",
	"bot.sl.err_bottom", "bot.sl.split_line", "bot.sl.new_cat_name", "bot.sl.preview_line_new",
	"bot.sl.preview_realistic", "bot.sl.preview_hint_new", "bot.sl.created_title", "bot.sl.old_empty",
	"bot.sl.undo_hint_new", "bot.sl.undone_new", "bot.sl.err_tier_forbidden", "bot.sl.err_name_taken",
	"bot.sl.err_too_many_tiers", "bot.sl.err_method_one", "bot.sl.split_btn", "bot.sl.split_params_btn",
}

var layoutAlgorithmKeys = []string{
	"layoutgen.algorithm.desirability", "layoutgen.algorithm.desirability.description",
	"layoutgen.param.share_template", "layoutgen.option.share_template.pyramid",
	"layoutgen.option.share_template.equal", "layoutgen.option.share_template.wide_middle",
	"layoutgen.param.cheaper_row_edges", "layoutgen.param.cheaper_row_edges.hint",
}

func init() {
	MessageKeys = append(MessageKeys, seatLayoutKeys...)
	MessageKeys = append(MessageKeys, layoutAlgorithmKeys...)
}
