/**
 * A calendar date as this deployment writes it, not as UTC happens to see it.
 *
 * Every date in Weekly is a plain yyyy-mm-dd: a week starts on a Monday, a
 * decision was made on a day. `Date.toISOString()` answers in UTC, so east of
 * Greenwich a local midnight is the previous day, and four places in this app
 * were quietly off by one because of it:
 *
 *   - the meeting agenda's week arrows, which stepped 2026-08-24 to 2026-08-30
 *     instead of 2026-08-31, landing the reader on a date that is not the start
 *     of any week and an agenda that finds nothing;
 *   - the decision form's default date, which offered yesterday to anybody
 *     recording a decision before 09:00;
 *   - the weekly period picker, which labelled the previous week "이번 주";
 *   - the timeline chart's week keys.
 *
 * The fix is to read the fields the browser already has in local time rather
 * than converting to another zone and slicing the text.
 */
export function localDate(date: Date): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

/** Today, as a yyyy-mm-dd in the reader's own calendar. */
export function todayLocal(): string {
  return localDate(new Date())
}

/**
 * Moves a yyyy-mm-dd by whole weeks, staying on the same weekday.
 *
 * Parsed with an explicit midnight so the string is read as a local date rather
 * than as UTC, which is the other half of the same mistake.
 */
export function shiftWeeks(day: string, weeks: number): string {
  const date = new Date(`${day}T00:00:00`)
  date.setDate(date.getDate() + weeks * 7)
  return localDate(date)
}
