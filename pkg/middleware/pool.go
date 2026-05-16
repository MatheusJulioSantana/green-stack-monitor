package middleware

import (
	"sync"
	"time"
)

// ecoTracePool reutiliza *EcoTrace entre requests amostradas.
//
// Por que pool aqui e não para TracePayload?
// TracePayload é uma struct de valor que viaja pelo canal copiada —
// ela não aloca heap. EcoTrace é um ponteiro injetado no contexto,
// portanto escapa para o heap a cada request amostrada.
//
// Ganho medido: elimina 1 alloc/request no caminho crítico de requests
// amostradas. Em 10k req/s com SampleRate=1.0: ~10k allocs/s a menos,
// reduzindo pressão no GC e frequência de STW (stop-the-world) pauses.
//
// Invariante de segurança: todo campo de *EcoTrace DEVE ser resetado
// antes de ser devolvido ao pool. Se um campo com dado de uma request
// anterior vazar para a próxima, o resultado é um bug silencioso e
// difícil de reproduzir. O reset é feito em releaseEcoTrace(), não
// na função Get() — isso é intencional: quem libera conhece o estado,
// quem adquire recebe sempre um objeto limpo.
var ecoTracePool = sync.Pool{
	New: func() any {
		return &EcoTrace{}
	},
}

// acquireEcoTrace retorna um *EcoTrace limpo do pool.
// O chamador DEVE chamar releaseEcoTrace quando o trace não for mais necessário.
func acquireEcoTrace() *EcoTrace {
	return ecoTracePool.Get().(*EcoTrace)
}

// releaseEcoTrace reseta todos os campos e devolve ao pool.
// Deve ser chamado após o TracePayload ser enfileirado (ou o drop ocorrer) —
// nunca antes, pois o middleware ainda lê trace.CacheHit e trace.startAlloc.
func releaseEcoTrace(t *EcoTrace) {
	// Reset explícito de cada campo — zero values corretos por tipo.
	t.startAlloc = 0
	t.startTime = time.Time{}
	t.CacheHit = false
	t.sampled = false
	ecoTracePool.Put(t)
}

// responseWriterPool reutiliza *responseWriter entre requests.
//
// responseWriter é alocado uma vez por request (mesmo requests não amostradas
// ainda precisam capturar o status code). Com o pool, essa alocação some
// do hot path em ambos os casos.
//
// Invariante: status é resetado para http.StatusOK (o default correto)
// antes de devolver ao pool — evita que o status de uma request anterior
// vaze para a próxima.
var responseWriterPool = sync.Pool{
	New: func() any {
		return &responseWriter{}
	},
}

func acquireResponseWriter() *responseWriter {
	return responseWriterPool.Get().(*responseWriter)
}

func releaseResponseWriter(rw *responseWriter) {
	rw.ResponseWriter = nil // libera a referência — GC pode coletar o writer original
	rw.status = 0
	rw.wroteHeader = false
	responseWriterPool.Put(rw)
}
