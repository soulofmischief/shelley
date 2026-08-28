import AppKit
import Foundation

@main
final class ShelleyApp: NSObject, NSApplicationDelegate {
    private var server: Process?
    private var readinessTimer: Timer?
    private var portFileURL: URL?
    private var shelleyURL: URL?
    private var logHandle: FileHandle?
    private var startupDeadline = Date.distantPast
    private var isShuttingDown = false

    static func main() {
        let application = NSApplication.shared
        let delegate = ShelleyApp()
        application.delegate = delegate
        application.setActivationPolicy(.regular)
        application.run()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        installApplicationMenu()

        do {
            try startServer()
        } catch {
            fail("Shelley could not start", detail: error.localizedDescription)
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        openShelley()
        return false
    }

    func applicationWillTerminate(_ notification: Notification) {
        isShuttingDown = true
        readinessTimer?.invalidate()
        removePortFile()
        if server?.isRunning == true {
            server?.terminate()
        }
        try? logHandle?.close()
    }

    private func installApplicationMenu() {
        let mainMenu = NSMenu()
        let applicationItem = NSMenuItem()
        let applicationMenu = NSMenu()

        let openItem = NSMenuItem(title: "Open Shelley", action: #selector(openShelley), keyEquivalent: "o")
        openItem.target = self
        applicationMenu.addItem(openItem)
        applicationMenu.addItem(.separator())

        let quitItem = NSMenuItem(
            title: "Quit Shelley",
            action: #selector(NSApplication.terminate(_:)),
            keyEquivalent: "q"
        )
        quitItem.target = NSApplication.shared
        applicationMenu.addItem(quitItem)

        applicationItem.submenu = applicationMenu
        mainMenu.addItem(applicationItem)
        NSApplication.shared.mainMenu = mainMenu
    }

    private func startServer() throws {
        let fileManager = FileManager.default
        guard let supportRoot = fileManager.urls(for: .applicationSupportDirectory, in: .userDomainMask).first,
              let libraryRoot = fileManager.urls(for: .libraryDirectory, in: .userDomainMask).first else {
            throw LauncherError.missingUserLibrary
        }

        let supportDirectory = supportRoot.appendingPathComponent("Shelley", isDirectory: true)
        let logDirectory = libraryRoot
            .appendingPathComponent("Logs", isDirectory: true)
            .appendingPathComponent("Shelley", isDirectory: true)
        try fileManager.createDirectory(at: supportDirectory, withIntermediateDirectories: true)
        try fileManager.createDirectory(at: logDirectory, withIntermediateDirectories: true)

        let logURL = logDirectory.appendingPathComponent("shelley.log")
        if !fileManager.fileExists(atPath: logURL.path) {
            guard fileManager.createFile(atPath: logURL.path, contents: nil) else {
                throw LauncherError.cannotCreateLog(logURL.path)
            }
        }
        let handle = try FileHandle(forWritingTo: logURL)
        try handle.seekToEnd()
        try handle.write(contentsOf: Data("\n--- Shelley launch \(Date()) ---\n".utf8))
        logHandle = handle

        let portURL = fileManager.temporaryDirectory
            .appendingPathComponent("shelley-port-\(UUID().uuidString)")
        portFileURL = portURL

        let binaryURL = Bundle.main.bundleURL
            .appendingPathComponent("Contents", isDirectory: true)
            .appendingPathComponent("MacOS", isDirectory: true)
            .appendingPathComponent("shelley-server")
        let process = Process()
        process.executableURL = binaryURL
        process.arguments = [
            "-db", supportDirectory.appendingPathComponent("shelley.db").path,
            "serve",
            "-port", "0",
            "-port-file", portURL.path,
        ]
        process.standardOutput = handle
        process.standardError = handle
        process.terminationHandler = { [weak self] process in
            DispatchQueue.main.async {
                self?.serverDidExit(status: process.terminationStatus, logURL: logURL)
            }
        }

        try process.run()
        server = process
        startupDeadline = Date().addingTimeInterval(30)

        let timer = Timer(timeInterval: 0.1, repeats: true) { [weak self] _ in
            self?.checkReadiness()
        }
        readinessTimer = timer
        RunLoop.main.add(timer, forMode: .common)
    }

    private func checkReadiness() {
        guard shelleyURL == nil, let portFileURL else {
            readinessTimer?.invalidate()
            return
        }

        if let contents = try? String(contentsOf: portFileURL, encoding: .utf8),
           let port = UInt16(contents.trimmingCharacters(in: .whitespacesAndNewlines)),
           let url = URL(string: "http://localhost:\(port)") {
            shelleyURL = url
            readinessTimer?.invalidate()
            removePortFile()
            openShelley()
            return
        }

        if Date() >= startupDeadline {
            fail("Shelley could not start", detail: "The server did not become ready within 30 seconds.")
        }
    }

    private func serverDidExit(status: Int32, logURL: URL) {
        guard !isShuttingDown else { return }
        fail(
            "Shelley stopped unexpectedly",
            detail: "The server exited with status \(status). Logs are available at \(logURL.path)."
        )
    }

    @objc private func openShelley() {
        guard let shelleyURL else { return }
        if !NSWorkspace.shared.open(shelleyURL) {
            fail("Shelley could not open", detail: "Open \(shelleyURL.absoluteString) in a browser.")
        }
    }

    private func fail(_ message: String, detail: String) {
        guard !isShuttingDown else { return }
        isShuttingDown = true
        readinessTimer?.invalidate()
        if server?.isRunning == true {
            server?.terminate()
        }

        NSApplication.shared.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.alertStyle = .critical
        alert.messageText = message
        alert.informativeText = detail
        alert.runModal()
        NSApplication.shared.terminate(nil)
    }

    private func removePortFile() {
        guard let portFileURL else { return }
        try? FileManager.default.removeItem(at: portFileURL)
        self.portFileURL = nil
    }
}

private enum LauncherError: LocalizedError {
    case missingUserLibrary
    case cannotCreateLog(String)

    var errorDescription: String? {
        switch self {
        case .missingUserLibrary:
            return "The user Library directory could not be found."
        case .cannotCreateLog(let path):
            return "The log file could not be created at \(path)."
        }
    }
}
