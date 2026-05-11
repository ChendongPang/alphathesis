import { useSyncExternalStore } from 'react'
import { parseThesisStream } from '../api/client'
import type { SSEEvent } from '../api/client'

export type StepKind = 'section' | 'pending' | 'ok' | 'error' | 'done'

export interface AnalysisStep {
  kind: StepKind
  text: string
}

export interface AnalysisSessionState {
  text: string
  steps: AnalysisStep[]
  active: boolean
  done: boolean
  error: string
  thesisId: number | null
}

const initialState: AnalysisSessionState = {
  text: '',
  steps: [],
  active: false,
  done: false,
  error: '',
  thesisId: null,
}

let state: AnalysisSessionState = initialState
let controller: AbortController | null = null
const listeners = new Set<() => void>()

function emit() {
  for (const listener of listeners) listener()
}

function setState(next: Partial<AnalysisSessionState>) {
  state = { ...state, ...next }
  emit()
}

function appendStep(step: AnalysisStep) {
  setState({ steps: [...state.steps, step] })
}

function handleEvent(ev: SSEEvent) {
  appendStep({ kind: ev.kind, text: ev.text })
  if (ev.id) {
    setState({ thesisId: ev.id })
  }
  if (ev.kind === 'done') {
    setState({ active: false, done: true })
  }
  if (ev.kind === 'error') {
    setState({ active: false, done: true, error: ev.text })
  }
}

export const analysisSession = {
  getSnapshot: () => state,

  subscribe: (listener: () => void) => {
    listeners.add(listener)
    return () => listeners.delete(listener)
  },

  start: (text: string) => {
    if (!text.trim() || state.active) return

    controller = new AbortController()
    state = {
      text,
      steps: [],
      active: true,
      done: false,
      error: '',
      thesisId: null,
    }
    emit()

    parseThesisStream(text, handleEvent, controller.signal).catch(err => {
      if (err?.name === 'AbortError') return
      appendStep({ kind: 'error', text: String(err) })
      setState({ active: false, done: true, error: String(err) })
    })
  },

  clear: () => {
    if (state.active) return
    controller = null
    state = initialState
    emit()
  },
}

export function useAnalysisSession() {
  return useSyncExternalStore(
    analysisSession.subscribe,
    analysisSession.getSnapshot,
    analysisSession.getSnapshot,
  )
}
