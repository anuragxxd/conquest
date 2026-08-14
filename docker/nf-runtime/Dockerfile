# Conquest Nextflow runtime base.
#
# Provides the JVM, a pinned Nextflow release and its pre-installed platform
# plugins. The conquest binary is deliberately NOT included -- that is layered
# on by docker/nf-runner/Dockerfile, which also sets USER, WORKDIR and
# ENTRYPOINT. This image is a base, not a runnable runner.
#
# Previously this image existed only as an opaque artifact in a private
# registry with no source in any repository, which is why it could not be
# rebuilt for other architectures. It was also carrying a conquest binary that
# nf-runner overwrote on every build -- a permanently stale copy that served no
# purpose.
#
# Everything here is architecture-portable: amazoncorretto publishes arm64,
# the Nextflow launcher is a shell script driving the JVM, and the plugins are
# JARs. This builds for linux/amd64 and linux/arm64.
#
# Rebuild only when bumping a Nextflow version or a plugin pin; the runner
# images on top are rebuilt on every commit.

ARG CORRETTO_TAG=21
FROM amazoncorretto:${CORRETTO_TAG}

# Nextflow release to pin. The get.nextflow.io installer honours NXF_VER.
ARG NXF_VER=24.10.4

# Plugins baked in so runner pods do not resolve them over the network at
# submit time. Pin per Nextflow version -- 25.10.x needs nf-ga4gh 1.4.0.
ARG NXF_PREINSTALL_PLUGINS="nf-ga4gh@1.3.0 nf-amazon@2.9.0"

RUN yum install -y procps-ng shadow-utils which git bash hostname \
    && yum clean all

RUN useradd -u 1000 -m conquest

ENV NXF_HOME=/.nextflow

RUN curl -fsSL get.nextflow.io | bash \
    && mv nextflow /usr/local/bin/nextflow \
    && chmod 755 /usr/local/bin/nextflow \
    && mkdir -p /.nextflow \
    && chown -R 1000:1000 /.nextflow

# Install as uid 1000 so the plugin cache is owned by the user the runner runs
# as; nextflow will not re-resolve them at runtime.
RUN set -eux; \
    for plugin in ${NXF_PREINSTALL_PLUGINS}; do \
        su conquest -c "nextflow plugin install ${plugin}"; \
    done; \
    chown -R 1000:1000 /.nextflow

RUN mkdir -p /workspace /tmp \
    && chown -R 1000:1000 /workspace /tmp

ENV NXF_ANSI_LOG=false
