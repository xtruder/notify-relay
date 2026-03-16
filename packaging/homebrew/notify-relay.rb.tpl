class NotifyRelay < Formula
  desc "Forward notify-send notifications to a host desktop session"
  homepage "https://github.com/xtruder/notify-relay"
  url "https://github.com/xtruder/notify-relay/archive/refs/tags/{{VERSION}}.tar.gz"
  sha256 "{{SHA256}}"
  license "Apache-2.0"

  depends_on "go" => :build

  def install
    timestamp = Time.now.utc.strftime("%Y-%m-%dT%H:%M:%SZ")
    ldflags = %W[
      -s
      -w
      -X github.com/xtruder/notify-relay/internal/buildinfo.Version=#{version}
      -X github.com/xtruder/notify-relay/internal/buildinfo.Commit=homebrew
      -X github.com/xtruder/notify-relay/internal/buildinfo.Date=#{timestamp}
    ]

    system "go", "build", *std_go_args(ldflags: ldflags), "-o", bin/"notify-relayd", "./cmd/notify-relayd"
    system "go", "build", *std_go_args(ldflags: ldflags), "-o", bin/"notify-send-proxy", "./cmd/notify-send-proxy"
    bin.install_symlink "notify-send-proxy" => "notify-send"
    pkgshare.install "packaging/systemd/notify-relayd.service"
  end

  test do
    assert_match "notify-send version=", shell_output("#{bin}/notify-send --version")
    assert_match "notify-relayd version=", shell_output("#{bin}/notify-relayd --version 2>&1")
  end

  def caveats
    <<~EOS
      `notify-send` proxy works on Linux and macOS clients.
      `notify-relayd` talks to org.freedesktop.Notifications and is intended for Linux hosts.
      The packaged systemd unit is installed at:
        #{pkgshare}/notify-relayd.service
    EOS
  end
end
