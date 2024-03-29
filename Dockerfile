FROM golang:1.21.2-alpine3.18 AS build

WORKDIR /api

COPY . .

RUN go mod download -x

RUN CGO_ENABLED=0 go build -v main.go


FROM alpine:3.18

WORKDIR /api

COPY --from=build /api/main /api/docs ./
RUN mkdir env
COPY --from=build /api/env/.env ./env

ENV GIN_MODE=release

CMD ["./main"]
