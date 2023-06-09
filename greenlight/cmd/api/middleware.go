package main

import (
	"errors"
	"expvar"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/felixge/httpsnoop"
	"golang.org/x/time/rate"
	"greenlight.wook.net/internal/data"
	"greenlight.wook.net/internal/validator"
)

func (app *application) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// defer 함수를 생성합니다(이 함수는 패닉이 발생했을 때 Go가 스택을 풀 때 항상 실행됩니다).
		defer func() {
			// builtin recover function을 사용하여 패닉이 발생했는지 여부를 확인합니다.
			if err := recover(); err != nil {
				// 패닉이 발생한 경우 응답에 "Connection: close"" 헤더를 설정합니다.
				// 이는 응답이 전송된 후 Go의 HTTP 서버가 현재 연결을 자동으로 닫도록 하는 트리거 역할을 합니다.
				w.Header().Set("Connection", "close")
				// recover()가 반환하는 값의 유형이 any이므로 fmt.Errorf()를 사용하여 오류로 정규화하고
				// serverErrorResponse() 헬퍼를 호출합니다. 그러면 오류 수준에서
				//사용자 정의 로거 유형을 사용하여 오류를 기록하고 클라이언트에게 500 내부 서버 오류 응답을 보냅니다.
				app.serverErrorResponse(w, r, fmt.Errorf("%s", err))
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func (app *application) rateLimit(next http.Handler) http.Handler {

	type client struct {
		limiter  *rate.Limiter
		lastSeen time.Time
	}

	var (
		mu      sync.Mutex
		clients = make(map[string]*client)
	)

	go func() {
		for {
			time.Sleep(time.Minute)
			mu.Lock()
			for ip, client := range clients {
				if time.Since(client.lastSeen) > 3*time.Minute {
					delete(clients, ip)
				}
			}
			// 중요한 것은 정리가 완료되면 뮤텍스의 잠금을 해제해야 한다는 것입니다.
			mu.Unlock()
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 속도 제한이 활성화된 경우에만 검사를 수행합니다.
		if app.config.limiter.enabled {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				app.serverErrorResponse(w, r, err)
				return
			}

			mu.Lock()

			if _, found := clients[ip]; !found {
				clients[ip] = &client{
					// config 구조체의 초당 요청 및 버스트 값을 사용합니다.
					limiter: rate.NewLimiter(rate.Limit(app.config.limiter.rps), app.config.limiter.burst),
				}

			}

			clients[ip].lastSeen = time.Now()

			if !clients[ip].limiter.Allow() {
				mu.Unlock()
				app.rateLimitExccededResponse(w, r)
				return
			}

			mu.Unlock()

		}

		next.ServeHTTP(w, r)
	})
}

func (app *application) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 응답에 "Vary: Authorization" 헤더를 추가합니다. 이는 캐시에게 요청의 Authorization 헤더 값에 따라 응답이 달라질 수 있음을 나타냅니다.
		w.Header().Add("Vary", "Authorization")

		// 요청에서 Authorization 헤더의 값 가져옵니다. 해당 헤더가 없는 경우 빈 문자열 ""을 반환합니다.
		authorizationHeader := r.Header.Get("Authorization")

		// Authorization 헤더가 없는 경우, 방금 만든 contextSetUser() 헬퍼를 사용하여 AnonymousUser를 요청 컨텍스트에 추가합니다.
		// 그런 다음 체인의 다음 핸들러를 호출하고 아래의 코드를 실행하지 않고 반환합니다.
		if authorizationHeader == "" {
			r = app.contextSetUser(r, data.AnonymousUser)
			next.ServeHTTP(w, r)
			return
		}

		// 그렇지 않은 경우, Authorization 헤더의 값은 "Bearer <token>" 형식이라고 가정합니다.
		// 이를 구성 요소로 분리하고, 헤더가 예상한 형식이 아닌 경우에는 invalidAuthenticationTokenResponse() 헬퍼를 사용하여
		// 401 Unauthorized 응답을 반환합니다. (이 헬퍼는 곧 생성할 것입니다.)
		headerParts := strings.Split(authorizationHeader, " ")
		if len(headerParts) != 2 || headerParts[0] != "Bearer" {
			app.invalidAuthenticationTokenResponse(w, r)
			return
		}

		// 헤더 구성 요소로부터 실제 인증 토큰을 추출합니다.
		token := headerParts[1]

		// 토큰이 올바른 형식인지 확인하기 위해 유효성 검사를 수행합니다.
		v := validator.New()

		// 토큰이 유효하지 않은 경우, 일반적으로 사용하는 failedValidationResponse() 헬퍼 대신
		// invalidAuthenticationTokenResponse() 헬퍼를 사용하여 응답을 전송합니다.
		if data.ValidateTokenPlaintext(v, token); !v.Valid() {
			app.invalidAuthenticationTokenResponse(w, r)
			return
		}

		// 인증 토큰과 관련된 사용자의 세부 정보를 검색합니다.
		// 일치하는 레코드가 없는 경우, invalidAuthenticationTokenResponse() 헬퍼를 호출합니다.
		// 중요: 여기에서 첫 번째 매개변수로 ScopeAuthentication을 사용하고 있음에 주목하세요.
		user, err := app.models.Users.GetForToken(data.ScopeAuthentication, token)
		if err != nil {
			switch {
			case errors.Is(err, data.ErrRecordNotFound):
				app.invalidAuthenticationTokenResponse(w, r)
			default:
				app.serverErrorResponse(w, r, err)
			}
			return
		}

		// Call the contextSetUser() helper to add the user information to the request
		// context.

		r = app.contextSetUser(r, user)
		// Call the next handler in the chain.
		next.ServeHTTP(w, r)
	})
}

// 사용자가 익명이 아닌지 확인하기 위해 새로운 requireAuthenticatedUser() 미들웨어를 생성합니다.
func (app *application) requireAuthenticatedUser(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := app.contextGetUser(r)
		if user.IsAnonymous() {
			app.authenticationRequiredResponse(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// 사용자가 인증되었고 활성화되었는지를 확인합니다.
func (app *application) requireActivatedUser(next http.HandlerFunc) http.HandlerFunc {
	// 이 http.HandlerFunc를 반환하는 대신에 fn 변수에 할당합니다.
	fn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := app.contextGetUser(r)
		// 사용자가 활성화되었는지 확인합니다.
		if !user.Activated {
			app.inactiveAccountResponse(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
	// 반환하기 전에 requireAuthenticatedUser() 미들웨어로 fn을 감싸줍니다.
	return app.requireAuthenticatedUser(fn)
}

// 미들웨어 함수의 첫 번째 매개 변수는 사용자가 가져야 하는 권한 코드입니다.
func (app *application) requirePermission(code string, next http.HandlerFunc) http.HandlerFunc {
	fn := func(w http.ResponseWriter, r *http.Request) {
		//요청 컨텍스트에서 사용자를 검색합니다.
		user := app.contextGetUser(r)

		// 사용자에 대한 permissions 슬라이스를 가져옵니다.
		permissions, err := app.models.Permissions.GetAllForUser(user.ID)
		if err != nil {
			app.serverErrorResponse(w, r, err)
			return
		}

		// 슬라이스에 필요한 권한이 포함되어 있는지 확인합니다. 포함되지 않은 경우 403 금지됨 응답을 반환합니다.
		if !permissions.Include(code) {
			app.notPermittedResponse(w, r)
			return
		}
		// 그렇지 않으면 필요한 권한이 있으므로 체인의 다음 핸들러를 호출합니다.
		next.ServeHTTP(w, r)
	}
	// 이를 반환하기 전에 requireActivatedUser() 미들웨어로 래핑합니다.
	return app.requireActivatedUser(fn)
}

func (app *application) enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		w.Header().Add("Vary", "Access-Control-Request-Method")
		origin := r.Header.Get("Origin")

		if origin != "" {
			for i := range app.config.cors.trustedOrigins {
				if origin == app.config.cors.trustedOrigins[i] {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					// 요청에 HTTP 메서드 OPTIONS가 있고 "Access-Control-Request-Method" 헤더가
					// 포함되어 있는지 확인합니다. 포함되어 있으면 사전 점검 요청으로 처리합니다.
					if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
						//  앞서 설명한 대로 필요한 비행 전 응답 헤더를 설정합니다.
						w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, PUT, PATCH, DELETE")
						w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
						//  200 OK 상태와 함께 헤더를 작성하고 추가 작업 없이 미들웨어에서 반환합니다.
						w.WriteHeader(http.StatusOK)
						return
					}
					break
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (app *application) metrics(next http.Handler) http.Handler {
	totalRequestsReceived := expvar.NewInt("total_requests_received")
	totalResponsesSent := expvar.NewInt("total_responses_sent")
	totalProcessingTimeMicroseconds := expvar.NewInt("total_processing_time_μs")

	// 각 HTTP 상태 코드에 대한 응답 수를 저장할 새 expvar 맵을 선언합니다.
	totalResponsesSentByStatus := expvar.NewMap("total_responses_sent_by_status")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 이전과 같이 수신된 요청 횟수를 늘립니다.
		totalRequestsReceived.Add(1)

		//기존 http.ResponseWriter 및 http.Request와 함께 체인의 다음 핸들러를
		//전달하여 httpsnoop.CaptureMetrics() 함수를 호출합니다. 그러면 위에서
		// 본 메트릭 구조체가 반환됩니다.
		metrics := httpsnoop.CaptureMetrics(next, w, r)

		totalResponsesSent.Add(1)
		// httpsnoop에서 요청 처리 시간(마이크로초)을 가져와 누적 처리 시간을 증가시킵니다.
		totalProcessingTimeMicroseconds.Add(metrics.Duration.Microseconds())

		//Add() 메서드를 사용하여 주어진 상태 코드의 카운트를 1씩 증가시킵니다.
		// expvar 맵은 문자열 키로 되어 있으므로, 상태 코드(정수인)를 문자열로 변환하려면
		// strconv.Itoa() 함수를 사용해야 한다는 점에 유의하세요.
		totalResponsesSentByStatus.Add(strconv.Itoa(metrics.Code), 1)
	})
}
