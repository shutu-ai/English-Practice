// Browser-only speech playback service. It intentionally has no dependency on
// the API or learning state: playback is a local UX aid, not a practice event.
(function (global) {
  const STORAGE_KEY = 'english-practice.speech'
  const MAX_CHARS = 1000
  const synth = global.speechSynthesis
  const supported = !!synth && typeof global.SpeechSynthesisUtterance === 'function'
  let voices = []
  let selectedVoiceId = ''
  let rate = 1
  const listeners = new Set()

  function readSettings() {
    try {
      const saved = JSON.parse(global.localStorage.getItem(STORAGE_KEY) || '{}')
      selectedVoiceId = typeof saved.voiceId === 'string' ? saved.voiceId : ''
      rate = clampRate(Number(saved.rate))
    } catch (_) {
      selectedVoiceId = ''
      rate = 1
    }
  }

  function saveSettings() {
    try { global.localStorage.setItem(STORAGE_KEY, JSON.stringify({ voiceId: selectedVoiceId, rate })) } catch (_) { /* private mode */ }
  }

  function clampRate(value) {
    if (!Number.isFinite(value)) return 1
    return Math.min(1.2, Math.max(0.7, value))
  }

  function refreshVoices() {
    voices = supported ? (synth.getVoices() || []) : []
    listeners.forEach(listener => listener(voices.slice()))
    return voices.slice()
  }

  function selectedVoice() {
    if (!voices.length) return null
    const byId = voices.find(voice => voice.voiceURI === selectedVoiceId || voice.name === selectedVoiceId)
    if (byId) return byId
    const english = voices.filter(voice => /^en(?:-|_)/i.test(voice.lang || ''))
    return english.find(voice => /^en-US/i.test(voice.lang || '')) || english[0] || voices[0]
  }

  function stop() {
    if (supported) synth.cancel()
  }

  function speak(text, callbacks) {
    const value = String(text || '').trim()
    if (!supported || !value || value.length > MAX_CHARS) return false
    stop()
    const utterance = new global.SpeechSynthesisUtterance(value)
    utterance.lang = 'en-US'
    utterance.rate = rate
    const voice = selectedVoice()
    if (voice) utterance.voice = voice
    const hooks = callbacks || {}
    utterance.onstart = () => { if (hooks.onstart) hooks.onstart() }
    utterance.onend = () => { if (hooks.onend) hooks.onend() }
    utterance.onerror = event => { if (hooks.onerror) hooks.onerror(event) }
    synth.speak(utterance)
    return true
  }

  function setVoice(id) {
    selectedVoiceId = String(id || '')
    saveSettings()
  }

  function setRate(value) {
    rate = clampRate(Number(value))
    saveSettings()
    return rate
  }

  function onVoicesChanged(listener) {
    if (typeof listener !== 'function') return () => {}
    listeners.add(listener)
    listener(voices.slice())
    return () => listeners.delete(listener)
  }

  readSettings()
  if (supported) {
    refreshVoices()
    if (typeof synth.addEventListener === 'function') synth.addEventListener('voiceschanged', refreshVoices)
    else synth.onvoiceschanged = refreshVoices
  }

  global.SpeechPlayback = Object.freeze({
    maxChars: MAX_CHARS,
    isSupported: () => supported,
    speak,
    stop,
    getVoices: () => voices.slice(),
    selectedVoice,
    selectedVoiceId: () => selectedVoiceId,
    rate: () => rate,
    setVoice,
    setRate,
    onVoicesChanged,
  })
})(window)
