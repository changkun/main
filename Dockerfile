# Copyright 2020 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

FROM alpine
# The ideas API calls the model gateway and the GitHub API over TLS.
RUN apk add --no-cache ca-certificates
COPY main /app/main
EXPOSE 80
CMD ["/app/main"]
