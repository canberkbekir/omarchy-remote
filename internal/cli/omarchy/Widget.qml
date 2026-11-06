import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui

// Omarchy Remote in the bar: a phone icon, lit while a phone uses the app and
// highlighted during a party. Clicking opens the card with the QR code and
// passcode. pc-remote.sh, written by setup, runs the installed program.
BarWidget {
  id: root
  moduleName: "canberkbekir.omarchy-remote"

  readonly property string pluginId: "canberkbekir.omarchy-remote"
  readonly property string helper: (Quickshell.env("HOME") || "") + "/.config/omarchy/plugins/" + pluginId + "/pc-remote.sh"
  property var info: ({ running: false, phone: false, party: false, players: 0, problem: "" })

  function refresh() {
    if (!statusProc.running) statusProc.running = true
  }

  function apply(raw) {
    try {
      root.info = JSON.parse(raw)
    } catch (e) {
      root.info = { running: false, phone: false, party: false, players: 0, problem: "Omarchy Remote didn't answer" }
    }
  }

  function tooltip() {
    var lines = []
    if (info.problem) lines.push(info.problem)
    else lines.push(info.phone ? "Omarchy Remote: a phone is connected" : "Omarchy Remote: no phone connected")
    if (info.party) {
      var friends = info.players === 1 ? "1 friend" : info.players + " friends"
      lines.push("Party " + info.code + ": " + friends + " joined")
    }
    lines.push("Click for the QR code and passcode")
    return lines.join("\n")
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  IpcHandler {
    target: "canberkbekir.omarchy-remote"

    function refresh(): void {
      root.broadcast("refresh")
    }
  }

  Process {
    id: statusProc
    command: ["bash", root.helper, "status", "--json"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: root.apply(text)
    }
  }

  // The phone polls every few seconds, so a connection shows up within one tick.
  Timer {
    interval: 5000
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: root.refresh()
  }

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: "󰄜"
    active: root.info.party === true
    dimmed: !root.info.phone
    tooltipText: root.tooltip()
    onPressed: function(b) {
      if (b === Qt.RightButton) root.refresh()
      else if (root.bar) root.bar.run("omarchy-shell shell toggle " + root.pluginId + " '{}'")
    }
  }
}
