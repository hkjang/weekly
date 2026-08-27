/**
 * importPanel holds the two decisions the Import screen got wrong by writing
 * them inline, where nothing could test them.
 *
 * The history list auto-selects the newest job. Clicking that job ran
 * `setDetail(undefined); setSelectedID(job.id)` — but the id was already the
 * selected one, so the effect that reloads the detail never re-ran while the
 * click had just thrown the loaded detail away. The panel sat on its spinner
 * for as long as the tab stayed open, with the reviewer's in-progress
 * corrections still in state and no longer reachable, and the unsaved-work
 * guard objecting to leaving.
 *
 * A failed reload had the same shape from the other direction: loadDetail was
 * called without a catch, so a request that did not come back left the same
 * spinner and said nothing.
 */

/** jobClick says what a click on a job in the history list should do. */
export function jobClick(selectedID: number | undefined, clickedID: number): 'ignore' | 'switch' {
  return selectedID === clickedID ? 'ignore' : 'switch'
}

/** panelState names what the right-hand panel shows, given what it has. */
export function panelState(selectedID: number | undefined, hasDetail: boolean, failed: boolean): 'empty' | 'failed' | 'loading' | 'detail' {
  if (!selectedID) return 'empty'
  if (failed) return 'failed'
  return hasDetail ? 'detail' : 'loading'
}
