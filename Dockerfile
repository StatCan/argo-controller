# Build with the golang image
FROM golang:1.22.5-alpine AS build

# Add git
RUN apk add git

# Set workdir
WORKDIR /src

# Add dependencies
COPY go.mod .
COPY go.sum .
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 go install .

# Generate final image
FROM alpine:3.19.2
RUN apk --update --no-cache add ca-certificates
COPY --from=build /go/bin/argo-controller /usr/local/bin/argo-controller
ENTRYPOINT [ "/usr/local/bin/argo-controller" ]
