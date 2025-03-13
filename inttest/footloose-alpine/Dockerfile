ARG ALPINE_VERSION
ARG GOLANG_IMAGE

FROM docker.io/library/alpine:$ALPINE_VERSION

ARG TARGETARCH
ARG KUBE_VERSION
ARG TROUBLESHOOT_VERSION=v0.61.0

# Apply our changes to the image
COPY root/ /

RUN apk add openrc openssh-server bash busybox-openrc coreutils curl inotify-tools sudo
# enable syslog and sshd
RUN rc-update add cgroups boot
RUN rc-update add syslog boot
RUN rc-update add machine-id boot
RUN rc-update add sshd default
RUN rc-update add local default
# Ensures that /usr/local/bin/k0s is seeded from /dist at startup
RUN rc-update add k0s-seed default

# remove -docker keyword so we actually mount cgroups in container
RUN sed -i -e '/keyword/s/-docker//' /etc/init.d/cgroups
# disable ttys
RUN sed -i -e 's/^\(tty[0-9]\)/# \1/' /etc/inittab
# enable root logins
RUN sed -i -e 's/^root:!:/root::/' /etc/shadow

# Put kubectl into place to ease up debugging
RUN curl -Lo /usr/local/bin/kubectl https://storage.googleapis.com/kubernetes-release/release/v$KUBE_VERSION/bin/linux/$TARGETARCH/kubectl \
  && chmod +x /usr/local/bin/kubectl
ENV KUBECONFIG=/var/lib/k0s/pki/admin.conf

# Install latest k0s
RUN curl -sSLf https://get.k0s.sh | K0S_VERSION=v1.31.3+k0s.0 sh
