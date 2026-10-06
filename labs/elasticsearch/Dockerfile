FROM docker.elastic.co/elasticsearch/elasticsearch:8.19.22@sha256:e98f9c3b09beb2fbb9eaf667d602df3f0e00bd3644138b8458dc17ba1a675595

# The plugin manager selects the plugin version matching this ES installation.
RUN bin/elasticsearch-plugin install --batch analysis-smartcn

# Keep IK matched to ES; verify the exact vendor package before installing.
RUN curl -fsSL https://get.infini.cloud/elasticsearch/analysis-ik/8.19.22 -o /tmp/analysis-ik.zip \
    && echo '2cf38b4e21af1514dc30b7b641d630f9cf3bfc02479985185e36050e61bfda23  /tmp/analysis-ik.zip' | sha256sum -c - \
    && bin/elasticsearch-plugin install --batch file:///tmp/analysis-ik.zip \
    && rm /tmp/analysis-ik.zip
