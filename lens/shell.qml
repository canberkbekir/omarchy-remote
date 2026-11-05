// The zoom lens: a live, sharpened circle that follows the pointer.
// pc-remote starts it with `qs -p <dir>` while Zoom is on and feeds it the
// pointer and the window under it over a socket, one line per change:
//   <x> <y> <window x> <window y> <window width> <window height> <address>
// It quits when that socket closes, so it never outlives pc-remote.
import QtQuick
import QtQuick.Effects
import Quickshell
import Quickshell.Io
import Quickshell.Wayland
import Quickshell.Hyprland

ShellRoot {
  id: root
  readonly property int lensSize: parseInt(Quickshell.env("PC_REMOTE_LENS_SIZE")) || 280
  readonly property real zoom: parseFloat(Quickshell.env("PC_REMOTE_LENS_ZOOM")) || 2
  // The phone app's theme colors, so the lens matches it.
  readonly property color ring: Quickshell.env("PC_REMOTE_LENS_RING") || "#C8F24A"
  readonly property color ground: Quickshell.env("PC_REMOTE_LENS_GROUND") || "#0E0F0C"
  property real cx: -100000
  property real cy: -100000
  property real wx: 0
  property real wy: 0
  property real ww: 0
  property real wh: 0
  property string address: ""
  readonly property string bareAddress: address.replace(/^0x/, "")
  // Window capture leaves out overlays, so the lens never magnifies itself.
  readonly property var target: Hyprland.toplevels.values.find(t => t.address.replace(/^0x/, "") === bareAddress) ?? null

  Socket {
    path: Quickshell.env("PC_REMOTE_LENS_SOCKET")
    connected: true
    onConnectedChanged: if (!connected) Qt.quit()
    parser: SplitParser {
      onRead: line => {
        const f = line.split(" ");
        if (f.length !== 7) return;
        root.cx = +f[0]; root.cy = +f[1];
        root.wx = +f[2]; root.wy = +f[3]; root.ww = +f[4]; root.wh = +f[5];
        if (f[6] !== root.address) {
          root.address = f[6];
          Hyprland.refreshToplevels();
        }
      }
    }
  }

  Variants {
    model: Quickshell.screens
    PanelWindow {
      id: win
      required property var modelData
      screen: modelData
      anchors { top: true; bottom: true; left: true; right: true }
      exclusionMode: ExclusionMode.Ignore
      WlrLayershell.layer: WlrLayer.Overlay
      WlrLayershell.namespace: "pc-remote-lens"
      color: "transparent"
      mask: Region {}

      readonly property real lx: root.cx - modelData.x
      readonly property real ly: root.cy - modelData.y
      readonly property bool here: lx >= 0 && ly >= 0 && lx < modelData.width && ly < modelData.height

      Item {
        id: lens
        visible: win.here
        width: root.lensSize
        height: root.lensSize
        x: win.lx - width / 2
        y: win.ly - height / 2

        // The window, scaled up and placed so the pointer sits in the middle.
        Item {
          id: content
          anchors.fill: parent
          visible: false
          clip: true
          layer.enabled: true
          Rectangle { anchors.fill: parent; color: root.ground }
          ScreencopyView {
            captureSource: win.here && root.target ? root.target.wayland : null
            live: true
            width: root.ww * root.zoom
            height: root.wh * root.zoom
            x: (root.wx - root.cx) * root.zoom + lens.width / 2
            y: (root.wy - root.cy) * root.zoom + lens.height / 2
          }
        }
        // Sharpens at the window's own pixel size, so edges and text stay crisp.
        ShaderEffect {
          id: sharpened
          anchors.fill: parent
          visible: false
          layer.enabled: true
          property var source: content
          property vector2d texel: Qt.vector2d(root.zoom / lens.width, root.zoom / lens.height)
          property real amount: 0.7
          fragmentShader: Qt.resolvedUrl("sharpen.frag.qsb")
        }
        Rectangle {
          id: circle
          anchors.fill: parent
          radius: width / 2
          visible: false
          layer.enabled: true
        }
        MultiEffect {
          anchors.fill: parent
          source: sharpened
          maskEnabled: true
          maskSource: circle
        }
        Rectangle {
          anchors.fill: parent
          radius: width / 2
          color: "transparent"
          border.width: 3
          border.color: root.ring
        }
        // The real pointer is hidden while the lens is up; this one marks
        // where a click lands.
        Canvas {
          x: parent.width / 2 - 1
          y: parent.height / 2 - 1
          width: 20
          height: 26
          onPaint: {
            const c = getContext("2d");
            c.beginPath();
            c.moveTo(1.5, 1.5);
            c.lineTo(1.5, 20);
            c.lineTo(6.5, 15.5);
            c.lineTo(10, 23.5);
            c.lineTo(13, 22.3);
            c.lineTo(9.6, 14.5);
            c.lineTo(16.5, 14.5);
            c.closePath();
            c.fillStyle = "#FFFFFF";
            c.strokeStyle = "#000000";
            c.lineWidth = 1.5;
            c.lineJoin = "round";
            c.fill();
            c.stroke();
          }
        }
      }
    }
  }
}
