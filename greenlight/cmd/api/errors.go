package main

import (
	"fmt"
	"net/http"
)

// logError() 메서드는 오류 메시지를 기록하는 generic 헬퍼입니다.
// 이 책의 뒷부분에서 구조화된 로깅을 사용하도록 업그레이드하고
// HTTP 메서드 및 URL을 포함한 요청에 대한 추가 정보를 기록할 것입니다.
func (app *application) logError(r *http.Request, err error) {
	app.logger.Print(err)
}

// errorResponse() 메서드는 주어진 상태 코드와 함께 JSON 형식의 오류 메시지를 클라이언트에 전송하는
// generic 헬퍼입니다. 메시지 매개변수에 문자열 유형이
// 아닌 임의의 유형을 사용하는 이유는 응답에 포함할 수 있는 값을 보다 유연하게 설정할 수 있기 때문입니다.
func (app *application) errorResponse(w http.ResponseWriter, r *http.Request, status int, message any) {
	env := envelope{"error": message}
	// writeJSON() 헬퍼를 사용하여 응답을 작성합니다. 이 과정에서 오류가 반환되면
	// 이를 기록하고 클라이언트에 500 내부 서버 오류 상태 코드가 포함된
	// 빈 응답을 보내는 것으로 되돌아갑니다.
	err := app.writeJSON(w, status, env, nil)
	if err != nil {
		app.logError(r, err)
		w.WriteHeader(500)
	}

}

// serverErrorResponse() 메서드는 애플리케이션이 런타임에 예기치 않은 문제가 발생했을 때
// 사용됩니다. 이 메서드는 자세한 오류 메시지를 기록한 다음 errorResponse() 헬퍼를
// 사용하여 500 내부 서버 오류 상태 코드와 (일반 오류 메시지가 포함된)
// JSON 응답을 클라이언트에 전송합니다.
func (app *application) serverErrorResponse(w http.ResponseWriter, r *http.Request, err error) {
	app.logError(r, err)

	message := "서버에 문제가 발생하여 요청을 처리할 수 없습니다."
	app.errorResponse(w, r, http.StatusInternalServerError, message)
}

// notFoundResponse() 메서드는 404 찾을 수 없음 상태 코드와
// JSON 응답을 클라이언트에 전송하는 데 사용됩니다.
func (app *application) notFoundResponse(w http.ResponseWriter, r *http.Request) {
	message := "요청한 리소스를 찾을 수 없습니다."
	app.errorResponse(w, r, http.StatusNotFound, message)
}

// NotAllowedResponse() 메서드는 405 메서드 허용되지 않음 상태 코드와 JSON 응답을 클라이언트에 전송하는 데 사용됩니다.
func (app *application) methodNotAllowedResponse(w http.ResponseWriter, r *http.Request) {
	message := fmt.Sprintf("이 리소스에는 %s 메서드가 지원되지 않습니다.", r.Method)
	app.errorResponse(w, r, http.StatusMethodNotAllowed, message)
}
