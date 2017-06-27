#!/bin/bash
set -xe

image_name="adapter"
release_type=$1
release_ver=$2

if [ "$release_type" == "" ];then
  echo "please input release type (community | enterprise | all ) and version"
  exit 1
fi

trap 'clean_tmp; exit' QUIT TERM EXIT

function clean_tmp() {
  echo "clean temporary file..."
  [ -f Dockerfile.release ] && rm -rf Dockerfile.release
}

function release(){
  release_name=$1      # enterprise | community
  release_version=$2   # 3.2 | 2017.05

  # 目前k8s及adapter还么有分企业和社区版本
  # git checkout ${release_name}-${release_version}

  echo "Pull newest code..." && sleep 3
  git pull

  # build
  docker run -v `pwd`:/go/src/k8s.io/kubernetes -w /go/src/k8s.io/kubernetes golang:1.7.3 go build -ldflags="-w" -o adapter/grproxy ./cmd/kube-proxy


  # get commit sha
  git_commit=$(git log -n 1 --pretty --format=%h)



  # get git describe info
  release_desc=${release_name}-${release_version}-${git_commit}

  sed "s/__RELEASE_DESC__/${release_desc}/" adapter/Dockerfile > Dockerfile.release

  docker build -t hub.goodrain.com/dc-deploy/${image_name}:${release_version} -f /adapter/Dockerfile.release adapter
  docker push hub.goodrain.com/dc-deploy/${image_name}:${release_version}
}

case $release_type in
"community")
    release $1 $release_ver
    ;;
"enterprise")
    release $1 $release_ver
    ;;
"all")
    release "community" $release_ver
    release "enterprise" $release_ver
    ;;
esac
