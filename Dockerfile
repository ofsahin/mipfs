FROM golang:1.7
RUN mkdir -p /go/src/github.com/kpmy/mipfs
COPY . /go/src/github.com/kpmy/mipfs
ENV GOPATH /go
RUN go get -v github.com/kpmy/mipfs/dav_cmd
RUN go install github.com/kpmy/mipfs/dav_cmd
RUN mkdir -p /go/.diskv
RUN printf "ipfs:5001" > /go/.diskv/ipfs
EXPOSE 6001
CMD dav_cmd