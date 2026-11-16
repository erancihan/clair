package pages

import "encoding/json"

// The host pages are forms more than anything, and every field and button on
// them looks the same. The classes live here once rather than on fifty inputs.
const (
	hostInputClass   = "w-full rounded-xl border border-zinc-200 bg-white px-3 py-2 text-sm text-zinc-800 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
	hostLabelClass   = "block text-xs font-medium text-zinc-500 dark:text-zinc-400"
	hostPanelClass   = "rounded-3xl border border-zinc-200 bg-white p-6 shadow-sm dark:border-zinc-800 dark:bg-zinc-800"
	hostPrimaryClass = "inline-flex items-center rounded-full bg-teal-600 px-5 py-2.5 text-sm font-medium text-white transition hover:bg-teal-500 disabled:cursor-not-allowed disabled:bg-zinc-300 dark:disabled:bg-zinc-700"
	hostDeleteClass  = "flex-none rounded-full px-3 py-1.5 text-sm font-medium text-red-600 ring-1 ring-red-200 transition hover:bg-red-50 disabled:opacity-50 dark:text-red-400 dark:ring-red-400/30 dark:hover:bg-red-400/10"
)

// hostScheduleAttr binds the schedule editor to its schedule. The slug goes
// through JSON rather than being spliced into the attribute, so a slug with a
// quote in it is data and not script.
func hostScheduleAttr(slug string) string {
	cfg, _ := json.Marshal(map[string]string{"slug": slug})
	return "hostSchedule(" + string(cfg) + ")"
}
