import QtQuick
import QtQuick.Layouts
import Quickshell
import Quickshell.Io
import Quickshell.Wayland
import qs.Commons
import qs.Ui

// The card the bar icon opens: the app's QR code to scan with the phone, its
// address, and the passcode behind a click. Esc or the scrim dismiss it.
// The passcode only enters shell memory when shown, and closing drops it.
Item {
  id: root

  property var shell: null
  property var manifest: null

  readonly property string pluginId: "canberkbekir.omarchy-remote"
  readonly property string helper: (Quickshell.env("HOME") || "") + "/.config/omarchy/plugins/" + pluginId + "/pc-remote.sh"

  property bool opened: false
  property bool loading: false
  property var info: ({})
  property string passcode: ""
  property bool passcodeVisible: false

  // With Tailscale and the home network there are two addresses; the labels
  // above the code pick one. An older helper gives only url and qr.
  readonly property var addresses: info.addresses || (info.url ? [{ label: info.label, url: info.url, qr: info.qr }] : [])
  property int pick: 0
  readonly property var current: addresses[pick] || addresses[0] || ({})
  readonly property var qrRows: current.qr || []
  readonly property int qrSize: qrRows.length
  // The quiet zone a scanner needs around the code, in modules.
  readonly property int quiet: 3

  readonly property color onScrim: "white"
  readonly property color onScrimDim: Qt.rgba(1, 1, 1, 0.55)
  readonly property color onScrimUrgent: "#ff6b6b"
  readonly property string fontFamily: Style.font.family

  function open(payloadJson) {
    root.info = {}
    root.loading = true
    root.passcode = ""
    root.passcodeVisible = false
    root.pick = 0
    if (!statusProc.running) statusProc.running = true
    root.opened = true
    // The window maps after this, so focus has to be taken again for Esc.
    Qt.callLater(function() {
      if (root.opened) keyCatcher.forceActiveFocus()
    })
  }

  function close() {
    root.opened = false
    root.passcode = ""
    root.passcodeVisible = false
  }

  function dismiss() {
    if (root.shell && typeof root.shell.hide === "function") root.shell.hide(root.pluginId)
    else close()
  }

  function togglePasscode() {
    if (passcodeVisible) { passcodeVisible = false; return }
    if (passcode !== "") { passcodeVisible = true; return }
    if (!passcodeProc.running) passcodeProc.running = true
  }

  function partyLine() {
    if (!info.party) return ""
    var friends = info.players === 1 ? "1 friend" : info.players + " friends"
    return "Party " + info.code + " · " + friends + " joined"
  }

  Process {
    id: statusProc
    command: ["bash", root.helper, "status", "--json", "--connect"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        try { root.info = JSON.parse(text) } catch (e) { root.info = { problem: "Omarchy Remote didn't answer" } }
        root.loading = false
      }
    }
  }

  Process {
    id: passcodeProc
    command: ["bash", root.helper, "passcode"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: {
        if (!root.opened) return
        root.passcode = String(text || "").trim()
        root.passcodeVisible = root.passcode !== ""
      }
    }
  }

  PanelWindow {
    visible: root.opened
    anchors { top: true; bottom: true; left: true; right: true }
    color: "transparent"
    exclusionMode: ExclusionMode.Ignore
    WlrLayershell.namespace: "omarchy-remote-card"
    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.keyboardFocus: WlrKeyboardFocus.Exclusive

    Rectangle {
      anchors.fill: parent
      color: Qt.rgba(0, 0, 0, 0.78)

      MouseArea {
        anchors.fill: parent
        onClicked: root.dismiss()
      }
    }

    Item {
      id: keyCatcher
      anchors.fill: parent
      focus: true

      Keys.onEscapePressed: root.dismiss()

      Item {
        anchors.centerIn: parent
        width: content.implicitWidth
        height: content.implicitHeight
        scale: Math.min(1,
          (keyCatcher.width - Style.space(32)) / Math.max(1, width),
          (keyCatcher.height - Style.space(32)) / Math.max(1, height))

        // Clicks on the card itself don't dismiss it.
        MouseArea { anchors.fill: parent; onClicked: {} }

        ColumnLayout {
          id: content
          anchors.fill: parent
          spacing: Style.space(16)

          Text {
            textFormat: Text.PlainText
            text: (root.info.name || "Omarchy Remote").toUpperCase()
            color: root.onScrimDim
            font.family: root.fontFamily
            font.pixelSize: Style.font.caption
            font.bold: true
            font.letterSpacing: 2
            elide: Text.ElideRight
            Layout.maximumWidth: Style.space(320)
            Layout.alignment: Qt.AlignHCenter
          }

          RowLayout {
            visible: root.addresses.length > 1
            spacing: Style.space(16)
            Layout.alignment: Qt.AlignHCenter

            Repeater {
              model: root.addresses

              Text {
                required property var modelData
                required property int index
                textFormat: Text.PlainText
                text: modelData.label
                color: root.onScrim
                opacity: index === root.pick ? 1 : 0.5
                font.family: root.fontFamily
                font.pixelSize: Style.font.bodySmall
                font.bold: index === root.pick
                font.underline: index === root.pick

                MouseArea {
                  anchors.fill: parent
                  cursorShape: Qt.PointingHandCursor
                  onClicked: root.pick = parent.index
                }
              }
            }
          }

          // One native rectangle per dark module keeps the code crisp.
          Rectangle {
            id: qrCanvas
            readonly property int modules: root.qrSize + 2 * root.quiet
            readonly property int moduleSize: root.qrSize > 0 ? Math.max(4, Math.floor(Style.space(240) / modules)) : 0

            visible: root.qrSize > 0
            width: modules * moduleSize
            height: width
            color: "white"
            radius: Style.cornerRadius
            Layout.alignment: Qt.AlignHCenter

            Grid {
              x: root.quiet * qrCanvas.moduleSize
              y: x
              columns: root.qrSize

              Repeater {
                model: root.qrSize * root.qrSize

                Rectangle {
                  required property int index
                  width: qrCanvas.moduleSize
                  height: qrCanvas.moduleSize
                  color: root.qrRows[Math.floor(index / root.qrSize)].charAt(index % root.qrSize) === "1" ? "#111111" : "transparent"
                }
              }
            }
          }

          Text {
            visible: root.loading
            text: "Loading…"
            color: root.onScrimDim
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            Layout.fillWidth: true
            horizontalAlignment: Text.AlignHCenter
          }

          Text {
            visible: root.qrSize > 0
            text: "Scan with your phone's camera"
            color: root.onScrimDim
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            Layout.fillWidth: true
            horizontalAlignment: Text.AlignHCenter
          }

          Text {
            textFormat: Text.PlainText
            visible: !!root.current.url
            text: root.current.url || ""
            color: root.onScrim
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            Layout.fillWidth: true
            Layout.maximumWidth: Style.space(320)
            horizontalAlignment: Text.AlignHCenter
            wrapMode: Text.WrapAnywhere
          }

          Text {
            textFormat: Text.PlainText
            visible: !root.loading && !root.info.problem
            text: root.passcodeVisible ? "Passcode  " + root.passcode : "Show passcode"
            color: root.onScrim
            opacity: root.passcodeVisible ? 1 : 0.6
            font.family: root.fontFamily
            font.pixelSize: root.passcodeVisible ? Style.font.body : Style.font.bodySmall
            Layout.fillWidth: true
            horizontalAlignment: Text.AlignHCenter

            MouseArea {
              anchors.fill: parent
              cursorShape: Qt.PointingHandCursor
              onClicked: root.togglePasscode()
            }
          }

          Text {
            textFormat: Text.PlainText
            visible: !root.loading && !root.info.problem
            text: root.partyLine() || (root.info.phone ? "A phone is connected" : "No phone connected")
            color: root.onScrimDim
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            Layout.fillWidth: true
            horizontalAlignment: Text.AlignHCenter
          }

          Text {
            textFormat: Text.PlainText
            visible: !!root.info.problem
            text: root.info.problem || ""
            color: root.onScrimUrgent
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            wrapMode: Text.Wrap
            Layout.fillWidth: true
            Layout.maximumWidth: Style.space(320)
            horizontalAlignment: Text.AlignHCenter
          }
        }
      }
    }
  }
}
