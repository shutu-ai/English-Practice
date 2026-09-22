export type SpeechHooks = { onstart?: () => void; onend?: () => void; onerror?: (event: SpeechSynthesisErrorEvent) => void }

export const SPEECH_SETTINGS_KEY = 'english-practice.speech'
export const MAX_SPEECH_CHARACTERS = 1000

export function createSpeechService() {
  const synthesis = typeof window !== 'undefined' ? window.speechSynthesis : undefined
  const supported = Boolean(synthesis && 'SpeechSynthesisUtterance' in window)
  let voices: SpeechSynthesisVoice[] = []
  let voiceId = ''
  let rate = 1
  const listeners = new Set<(voices: SpeechSynthesisVoice[]) => void>()

  const clampRate = (value: number) => Number.isFinite(value) ? Math.min(1.2, Math.max(0.7, value)) : 1
  const persist = () => { try { localStorage.setItem(SPEECH_SETTINGS_KEY, JSON.stringify({ voiceId, rate })) } catch (_) {} }
  try {
    const saved = JSON.parse(localStorage.getItem(SPEECH_SETTINGS_KEY) || '{}')
    voiceId = typeof saved.voiceId === 'string' ? saved.voiceId : ''
    rate = clampRate(Number(saved.rate))
  } catch (_) {}

  const refresh = () => {
    voices = supported ? synthesis!.getVoices() : []
    listeners.forEach(listener => listener([...voices]))
  }
  const selectedVoice = () => {
    const explicit = voices.find(voice => voice.voiceURI === voiceId || voice.name === voiceId)
    if (explicit) return explicit
    const english = voices.filter(voice => /^en(?:-|_)/i.test(voice.lang))
    return english.find(voice => /^en-US/i.test(voice.lang)) || english[0] || voices[0]
  }
  if (supported) {
    refresh()
    window.speechSynthesis.addEventListener('voiceschanged', refresh)
  }
  return {
    maxCharacters: MAX_SPEECH_CHARACTERS,
    isSupported: () => supported,
    getVoices: () => [...voices],
    selectedVoice,
    getRate: () => rate,
    setVoice: (id: string) => { voiceId = id; persist() },
    setRate: (value: number) => { rate = clampRate(value); persist(); return rate },
    subscribe: (listener: (voices: SpeechSynthesisVoice[]) => void) => { listeners.add(listener); listener([...voices]); return () => listeners.delete(listener) },
    stop: () => { if (supported) synthesis!.cancel() },
    speak: (text: string, hooks: SpeechHooks = {}) => {
      const value = text.trim()
      if (!supported || !value || value.length > MAX_SPEECH_CHARACTERS) return false
      synthesis!.cancel()
      const utterance = new SpeechSynthesisUtterance(value)
      utterance.lang = 'en-US'
      utterance.rate = rate
      const voice = selectedVoice()
      if (voice) utterance.voice = voice
      utterance.onstart = hooks.onstart || null
      utterance.onend = hooks.onend || null
      utterance.onerror = hooks.onerror || null
      synthesis!.speak(utterance)
      return true
    },
  }
}
