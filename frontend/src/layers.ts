/**
 * Which dismissible layer Escape belongs to.
 *
 * Overlays are siblings in the React tree — a report detail can have the
 * presentation deck open on top of it, and the command palette can open over
 * anything — but each of them listened for Escape independently. One keypress
 * therefore closed every open layer at once: leaving a deck also threw away the
 * report the reviewer was reading underneath it.
 *
 * Layers register here in the order they open, and only the top one acts.
 */

let stack: symbol[] = []

export function pushLayer(): symbol {
  const id = Symbol('layer')
  stack = [...stack, id]
  return id
}

export function popLayer(id: symbol) {
  stack = stack.filter(entry => entry !== id)
}

export function isTopLayer(id: symbol) {
  return stack.length > 0 && stack[stack.length - 1] === id
}

