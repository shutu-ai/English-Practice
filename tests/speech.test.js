import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'

const source = fs.readFileSync(new URL('../web/dist/speech.js', import.meta.url), 'utf8')

function createWindow({ supported = true } = {}) {
  const store = new Map()
  const utterances = []
  const synth = supported ? {
    current: null,
    getVoices: () => [{ name: 'US English', voiceURI: 'us', lang: 'en-US' }, { name: 'German', voiceURI: 'de', lang: 'de-DE' }],
    cancel: () => { synth.cancelled = true },
    speak: utterance => { synth.current = utterance; utterances.push(utterance) },
    addEventListener: () => {},
  } : undefined
  class MockUtterance {
    constructor(text) { this.text = text }
  }
  const window = {
    speechSynthesis: synth,
    SpeechSynthesisUtterance: supported ? MockUtterance : undefined,
    localStorage: { getItem: key => store.get(key) || null, setItem: (key, value) => store.set(key, value) },
  }
  const context = vm.createContext({ window, JSON, Number, String, Math, Set })
  vm.runInContext(source, context)
  return { playback: window.SpeechPlayback, synth, utterances, store }
}

test('unsupported browsers expose a safe no-op service', () => {
  const { playback } = createWindow({ supported: false })
  assert.equal(playback.isSupported(), false)
  assert.equal(playback.speak('hello'), false)
})

test('speech service trims, cancels, uses English fallback, and persists settings', () => {
  const { playback, synth, utterances, store } = createWindow()
  assert.equal(playback.isSupported(), true)
  playback.setRate(0.8)
  playback.setVoice('missing')
  assert.equal(playback.speak('  Hello there  '), true)
  assert.equal(synth.cancelled, true)
  assert.equal(utterances.at(-1).text, 'Hello there')
  assert.equal(utterances.at(-1).lang, 'en-US')
  assert.equal(utterances.at(-1).rate, 0.8)
  assert.equal(utterances.at(-1).voice.lang, 'en-US')
  assert.deepEqual(JSON.parse(store.get('english-practice.speech')), { voiceId: 'missing', rate: 0.8 })
})

test('speech service rejects empty and overlong text', () => {
  const { playback, utterances } = createWindow()
  assert.equal(playback.speak('   '), false)
  assert.equal(playback.speak('x'.repeat(playback.maxChars + 1)), false)
  assert.equal(utterances.length, 0)
})
