import AppKit
import CryptoKit
import Foundation
import Security
import Darwin

private let appID = 440
private let contentDepot = 441
private let engineDepot = 232251
private let keychainService = "com.teamfrontress.DepotDownloader"
private let keychainAccount = "account.config"
private let completionFile = ".team-frontress-complete"
private let qrPrompt = "Use the Steam Mobile App to sign in with this QR code:"
private let downloaderURL = URL(string: "https://github.com/SteamRE/DepotDownloader/releases/download/DepotDownloader_3.4.0/DepotDownloader-macos-x64.zip")!
private let downloaderArchiveSHA256 = "3214b689564d73e9342a8a4aef693de6ad3d293801b0f300a4466f60ec75befb"
private let downloaderExecutableSHA256 = "8433ea659f93fffb3c0da3400c2fd71393238db35d7780cf5a449fe8b8b10dba"

private struct Options {
    let support: URL

    static func parse(_ arguments: [String]) -> Options? {
        guard arguments.count == 1, let passwordEntry = getpwuid(getuid()),
              let homePointer = passwordEntry.pointee.pw_dir else { return nil }
        let home = URL(fileURLWithPath: String(cString: homePointer), isDirectory: true)
        return Options(support: home
            .appendingPathComponent("Library/Application Support", isDirectory: true)
            .appendingPathComponent("Team Frontress", isDirectory: true)
            .standardizedFileURL)
    }
}

private struct SavedState: Codable {
    let app: Int
    let contentDepot: Int
    let contentManifest: UInt64
    let engineDepot: Int
    let engineManifest: UInt64
    let verified: Bool
    let checkedAt: String
}

private struct RuntimePaths {
    let runtime: URL
    let state: URL
    let contentFinal: URL
    let engineFinal: URL
    let contentWork: URL
    let engineWork: URL
    let previous: URL
    let helperHome: URL
    let authCheck: URL
    let steamAccount: URL
    let authConfigPath: URL
    let promoting: URL
    let lock: URL
    let tools: URL
    let downloader: URL

    init(support: URL) {
        runtime = support.appendingPathComponent("runtime", isDirectory: true)
        state = runtime.appendingPathComponent("state.json")
        contentFinal = runtime.appendingPathComponent("depot-441", isDirectory: true)
        engineFinal = runtime.appendingPathComponent("depot-232251", isDirectory: true)
        contentWork = runtime.appendingPathComponent(".download/depot-441", isDirectory: true)
        engineWork = runtime.appendingPathComponent(".download/depot-232251", isDirectory: true)
        previous = runtime.appendingPathComponent(".previous", isDirectory: true)
        helperHome = runtime.appendingPathComponent("helper-home", isDirectory: true)
        authCheck = runtime.appendingPathComponent(".auth-check", isDirectory: true)
        steamAccount = runtime.appendingPathComponent("steam-account")
        authConfigPath = runtime.appendingPathComponent("auth-config-path")
        promoting = runtime.appendingPathComponent(".promoting.json")
        lock = runtime.appendingPathComponent(".helper.lock")
        tools = runtime.appendingPathComponent("tools", isDirectory: true)
        downloader = tools.appendingPathComponent("DepotDownloader")
    }

    func final(for depot: Int) -> URL {
        depot == contentDepot ? contentFinal : engineFinal
    }

    func work(for depot: Int) -> URL {
        depot == contentDepot ? contentWork : engineWork
    }

    func marker(for depot: Int, in root: URL) -> URL {
        if depot == contentDepot {
            return root.appendingPathComponent("tf/tf2_misc_dir.vpk")
        }
        return root.appendingPathComponent("bin/x64/launcher.dll")
    }
}

private enum HelperError: LocalizedError {
    case message(String)

    var errorDescription: String? {
        switch self {
        case .message(let text): return text
        }
    }
}

private func fail(_ message: String) throws -> Never {
    throw HelperError.message(message)
}

private func fileExists(_ url: URL) -> Bool {
    FileManager.default.fileExists(atPath: url.path)
}

private func regularFileExists(_ url: URL) -> Bool {
    (try? url.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true
}

private func decodeState(at url: URL) -> SavedState? {
    guard let data = try? Data(contentsOf: url) else { return nil }
    return try? JSONDecoder().decode(SavedState.self, from: data)
}

private func validState(_ state: SavedState, paths: RuntimePaths, freshOnly: Bool) -> Bool {
    guard state.verified, state.app == appID,
          state.contentDepot == contentDepot, state.engineDepot == engineDepot,
          state.contentManifest > 0, state.engineManifest > 0,
          regularFileExists(paths.marker(for: contentDepot, in: paths.contentFinal)),
          regularFileExists(paths.marker(for: engineDepot, in: paths.engineFinal)) else {
        return false
    }
    guard freshOnly else { return true }
    let formatter = ISO8601DateFormatter()
    guard let checked = formatter.date(from: state.checkedAt) else { return false }
    let age = Date().timeIntervalSince(checked)
    return age >= 0 && age < 6 * 60 * 60
}

private func isFreshInstall(_ paths: RuntimePaths) -> Bool {
    guard !fileExists(paths.promoting), let state = decodeState(at: paths.state) else {
        return false
    }
    return validState(state, paths: paths, freshOnly: true)
}

private func writeAtomically(_ data: Data, to url: URL, mode: mode_t? = nil) throws {
    try data.write(to: url, options: .atomic)
    if let mode, chmod(url.path, mode) != 0 {
        try fail("Could not secure \(url.lastPathComponent): \(String(cString: strerror(errno))).")
    }
}

private func writeText(_ text: String, to url: URL, mode: mode_t? = nil) throws {
    guard let data = text.data(using: .utf8) else {
        try fail("Could not encode \(url.lastPathComponent).")
    }
    try writeAtomically(data, to: url, mode: mode)
}

private final class RuntimeLock {
    private var descriptor: Int32 = -1

    init(url: URL, isCancelled: () -> Bool) throws {
        descriptor = Darwin.open(url.path, O_CREAT | O_RDWR, S_IRUSR | S_IWUSR)
        guard descriptor >= 0 else {
            try fail("Could not open the content helper lock: \(String(cString: strerror(errno))).")
        }
        while Darwin.lockf(descriptor, F_TLOCK, 0) != 0 {
            if errno != EAGAIN && errno != EACCES {
                let reason = String(cString: strerror(errno))
                Darwin.close(descriptor)
                descriptor = -1
                try fail("Could not lock the content runtime: \(reason).")
            }
            if isCancelled() {
                Darwin.close(descriptor)
                descriptor = -1
                try fail("Preparation was canceled.")
            }
            Thread.sleep(forTimeInterval: 0.2)
        }
    }

    deinit {
        if descriptor >= 0 {
            _ = Darwin.lockf(descriptor, F_ULOCK, 0)
            Darwin.close(descriptor)
        }
    }
}

private enum KeychainStore {
    private static var baseQuery: [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: keychainService,
            kSecAttrAccount as String: keychainAccount
        ]
    }

    static func read() throws -> Data? {
        var query = baseQuery
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else {
            try fail("Could not read DepotDownloader credentials from Keychain (\(status)).")
        }
        return data
    }

    static func write(_ data: Data) throws {
        let update: [String: Any] = [kSecValueData as String: data]
        var status = SecItemUpdate(baseQuery as CFDictionary, update as CFDictionary)
        if status == errSecItemNotFound {
            var item = baseQuery
            item[kSecValueData as String] = data
            item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            status = SecItemAdd(item as CFDictionary, nil)
        }
        guard status == errSecSuccess else {
            try fail("Could not save DepotDownloader credentials in Keychain (\(status)).")
        }
    }
}

private final class OutputCapture {
    private let lock = NSLock()
    private var currentDepot: Int?
    private var qrLines: [String] = []
    private var collectingQR = false
    private(set) var manifests: [Int: UInt64] = [:]
    private(set) var downloadedDepots: Set<Int> = []
    private(set) var accountName: String?
    private(set) var tokenRejected = false
    private(set) var authorizationRejected = false
    var onProgress: ((Double) -> Void)?
    var onManifest: ((Int, UInt64) -> Void)?
    var onQR: ((String) -> Void)?
    var onActivity: (() -> Void)?

    func consume(_ originalLine: String, isStandardOutput: Bool) -> Bool {
        let line = Self.withoutANSI(originalLine)
        lock.lock()
        defer { lock.unlock() }

        if let depot = Self.firstCapture(in: line, pattern: #"Processing depot ([0-9]+)"#).flatMap(Int.init) {
            currentDepot = depot
        }
        if let idText = Self.firstCapture(in: line, pattern: #"Manifest ([0-9]+) \("#),
           let id = UInt64(idText), let depot = currentDepot {
            manifests[depot] = id
            onManifest?(depot, id)
        }
        if let depot = Self.firstCapture(in: line, pattern: #"Depot ([0-9]+) - Downloaded"#).flatMap(Int.init) {
            downloadedDepots.insert(depot)
        }
        if let name = Self.firstCapture(
            in: line,
            pattern: #"Success! Next time you can login with -username ([A-Za-z0-9_]+) -remember-password instead of -qr\."#
        ) {
            accountName = name
        }
        if line.contains("Access token was rejected (") {
            tokenRejected = true
        }
        if line.contains("Enter account password for") {
            authorizationRejected = true
        }
        if let percentText = Self.firstCapture(in: line, pattern: #"([0-9]{1,3}\.[0-9]{2})%"#),
           let percent = Double(percentText) {
            onProgress?(min(max(percent, 0), 100))
        }
        if line.contains("Pre-allocating ") {
            onActivity?()
        }

        guard isStandardOutput else { return true }
        if line.contains(qrPrompt) {
            qrLines.removeAll(keepingCapacity: true)
            collectingQR = true
            onQR?("")
            return true
        }
        guard collectingQR else { return true }
        if Self.isQRLine(line) {
            qrLines.append(line)
            onQR?(qrLines.joined(separator: "\n"))
            // A live login challenge does not belong in the persistent log.
            return false
        } else if !qrLines.isEmpty {
            collectingQR = false
        }
        return true
    }

    private static func withoutANSI(_ string: String) -> String {
        string.replacingOccurrences(
            of: #"\u001B\[[0-9;?]*[ -/]*[@-~]"#,
            with: "",
            options: .regularExpression
        )
    }

    private static func firstCapture(in string: String, pattern: String) -> String? {
        guard let expression = try? NSRegularExpression(pattern: pattern),
              let match = expression.firstMatch(
                in: string,
                range: NSRange(string.startIndex..., in: string)
              ), match.numberOfRanges > 1,
              let range = Range(match.range(at: 1), in: string) else {
            return nil
        }
        return String(string[range])
    }

    private static func isQRLine(_ line: String) -> Bool {
        var hasBlock = false
        for scalar in line.unicodeScalars {
            if scalar.value == 0x20 || scalar.value == 0x09 { continue }
            if (0x2580...0x259f).contains(scalar.value) {
                hasBlock = true
                continue
            }
            return false
        }
        return hasBlock || !line.isEmpty
    }
}

private final class LineReader {
    private let lock = NSLock()
    private var pending = Data()
    private let isStandardOutput: Bool
    private let capture: OutputCapture
    private let logLock: NSLock

    init(isStandardOutput: Bool, capture: OutputCapture, logLock: NSLock) {
        self.isStandardOutput = isStandardOutput
        self.capture = capture
        self.logLock = logLock
    }

    func append(_ data: Data) {
        lock.lock()
        pending.append(data)
        while let index = pending.firstIndex(where: { $0 == 10 || $0 == 13 }) {
            let lineData = pending[..<index]
            pending.removeSubrange(...index)
            if !lineData.isEmpty {
                emit(String(decoding: lineData, as: UTF8.self))
            }
        }
        lock.unlock()
    }

    func finish() {
        lock.lock()
        if !pending.isEmpty {
            emit(String(decoding: pending, as: UTF8.self))
            pending.removeAll()
        }
        lock.unlock()
    }

    private func emit(_ line: String) {
        guard capture.consume(line, isStandardOutput: isStandardOutput),
              let data = (line + "\n").data(using: .utf8) else { return }
        logLock.lock()
        try? FileHandle.standardOutput.write(contentsOf: data)
        logLock.unlock()
    }
}

private struct ChildResult {
    let status: Int32
    let capture: OutputCapture
}

private final class HelperController: NSObject, NSApplicationDelegate {
    private let paths: RuntimePaths
    private let fileManager = FileManager.default
    private let processLock = NSLock()
    private var activeProcess: Process?
    private var cancelled = false
    private var windowWasShown = false

    private var window: NSWindow!
    private var detailLabel: NSTextField!
    private var progress: NSProgressIndicator!
    private var qrView: NSTextView!
    private var qrScroll: NSScrollView!
    private var disclosure: NSTextField!
    private var cancelButton: NSButton!

    var exitCode: Int32 = 1

    init(options: Options, paths: RuntimePaths) {
        self.paths = paths
        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildWindow()
        let existing = regularFileExists(paths.marker(for: contentDepot, in: paths.contentFinal)) &&
            regularFileExists(paths.marker(for: engineDepot, in: paths.engineFinal))
        if !existing {
            showWindow()
        }
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            run()
        }
    }

    private func buildWindow() {
        precondition(Thread.isMainThread)
        window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 600, height: 175),
            styleMask: [.titled],
            backing: .buffered,
            defer: false
        )
        window.title = "Team Frontress"
        window.isReleasedWhenClosed = false
        window.center()

        let title = NSTextField(labelWithString: "Preparing Team Fortress 2 content...")
        title.font = .systemFont(ofSize: 18, weight: .semibold)
        detailLabel = NSTextField(wrappingLabelWithString: "Checking the installed content...")
        detailLabel.textColor = .secondaryLabelColor

        progress = NSProgressIndicator()
        progress.style = .bar
        progress.minValue = 0
        progress.maxValue = 100
        progress.isIndeterminate = true
        progress.startAnimation(nil)

        disclosure = NSTextField(wrappingLabelWithString:
            "This Steam sign-in QR is shown by the verified, unmodified, open-source " +
            "DepotDownloader. Approve it only in the official Steam Mobile app. Team " +
            "Frontress never receives your password or Steam Client files. Steam checks " +
            "access only to TF2 depots 441 and 232251; the resulting token is kept in " +
            "macOS Keychain."
        )
        disclosure.textColor = .secondaryLabelColor
        disclosure.isHidden = true

        qrView = NSTextView(frame: .zero)
        qrView.isEditable = false
        qrView.isSelectable = true
        qrView.drawsBackground = false
        qrView.font = NSFont.monospacedSystemFont(ofSize: 7.5, weight: .regular)
        qrView.textContainerInset = NSSize(width: 8, height: 8)
        qrView.isHorizontallyResizable = true
        qrView.isVerticallyResizable = true
        qrView.textContainer?.widthTracksTextView = false
        qrView.textContainer?.containerSize = NSSize(
            width: CGFloat.greatestFiniteMagnitude,
            height: CGFloat.greatestFiniteMagnitude
        )

        qrScroll = NSScrollView()
        qrScroll.documentView = qrView
        qrScroll.hasVerticalScroller = true
        qrScroll.hasHorizontalScroller = true
        qrScroll.borderType = .bezelBorder
        qrScroll.translatesAutoresizingMaskIntoConstraints = false
        qrScroll.heightAnchor.constraint(equalToConstant: 335).isActive = true
        qrScroll.isHidden = true

        cancelButton = NSButton(title: "Cancel", target: self, action: #selector(cancel))
        cancelButton.bezelStyle = .rounded
        let buttons = NSStackView(views: [NSView(), cancelButton])
        buttons.orientation = .horizontal

        let stack = NSStackView(views: [title, detailLabel, progress, disclosure, qrScroll, buttons])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 12
        stack.edgeInsets = NSEdgeInsets(top: 22, left: 24, bottom: 20, right: 24)
        stack.translatesAutoresizingMaskIntoConstraints = false
        window.contentView = stack
        NSLayoutConstraint.activate([
            stack.widthAnchor.constraint(equalToConstant: 600),
            progress.widthAnchor.constraint(equalTo: stack.widthAnchor, constant: -48),
            detailLabel.widthAnchor.constraint(equalTo: progress.widthAnchor),
            disclosure.widthAnchor.constraint(equalTo: progress.widthAnchor),
            qrScroll.widthAnchor.constraint(equalTo: progress.widthAnchor),
            buttons.widthAnchor.constraint(equalTo: progress.widthAnchor)
        ])
    }

    private func showWindow() {
        precondition(Thread.isMainThread)
        guard !windowWasShown else { return }
        windowWasShown = true
        NSApp.setActivationPolicy(.regular)
        window.center()
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func showAuthorization() {
        DispatchQueue.main.async { [self] in
            detailLabel.stringValue = "Sign in to Steam to authorize access to the required TF2 depots."
            progress.isIndeterminate = true
            progress.startAnimation(nil)
            disclosure.isHidden = false
            qrScroll.isHidden = false
            qrView.string = "Waiting for Steam to generate a QR code..."
            window.setContentSize(NSSize(width: 600, height: 545))
            window.center()
            showWindow()
        }
    }

    private func showDownload(depot: Int, index: Int) {
        DispatchQueue.main.async { [self] in
            disclosure.isHidden = true
            qrScroll.isHidden = true
            if window.frame.height > 300 {
                window.setContentSize(NSSize(width: 600, height: 175))
                window.center()
            }
            detailLabel.stringValue = depot == contentDepot
                ? "Preparing TF2 content depot 441..."
                : "Preparing TF2 engine depot 232251..."
            progress.stopAnimation(nil)
            progress.isIndeterminate = false
            progress.doubleValue = Double(index) * 50
        }
    }

    private func updateProgress(_ percent: Double, index: Int) {
        DispatchQueue.main.async { [self] in
            progress.doubleValue = (Double(index) + percent / 100) * 50
        }
    }

    private func revealDownload() {
        DispatchQueue.main.async { [self] in
            showWindow()
        }
    }

    private func updateQR(_ text: String) {
        DispatchQueue.main.async { [self] in
            if !text.isEmpty {
                qrView.string = text
                qrView.scrollToBeginningOfDocument(nil)
            }
        }
    }

    @objc private func cancel() {
        processLock.lock()
        cancelled = true
        let process = activeProcess
        processLock.unlock()
        cancelButton.isEnabled = false
        detailLabel.stringValue = "Canceling..."
        if let process { terminate(process) }
    }

    private func isCancelled() -> Bool {
        processLock.lock()
        defer { processLock.unlock() }
        return cancelled
    }

    private func setActiveProcess(_ process: Process?) {
        processLock.lock()
        activeProcess = process
        let shouldTerminate = cancelled && process != nil
        processLock.unlock()
        if shouldTerminate, let process { terminate(process) }
    }

    private func terminate(_ process: Process) {
        process.terminate()
        DispatchQueue.global().asyncAfter(deadline: .now() + 3) {
            if process.isRunning { kill(process.processIdentifier, SIGKILL) }
        }
    }

    private func run() {
        do {
            try prepareRuntime()
            let runtimeLock = try RuntimeLock(url: paths.lock, isCancelled: isCancelled)
            defer { withExtendedLifetime(runtimeLock) {} }
            try checkCancellation()
            try captureCredentialFile(required: false)
            try ensureDownloader()
            if isFreshInstall(paths) {
                finishSuccess()
                return
            }
            try checkCancellation()
            try recoverPromotionIfNeeded()
            if isFreshInstall(paths) {
                finishSuccess()
                return
            }

            var account = try loadUsableAccount()
            var usedQRRetry = false
            if account == nil {
                account = try authorizeWithQR()
                usedQRRetry = true
            }

            try prepareWorkDirectory(for: contentDepot)
            try prepareWorkDirectory(for: engineDepot)

            var manifests: [Int: UInt64] = [:]
            for (index, depot) in [contentDepot, engineDepot].enumerated() {
                showDownload(depot: depot, index: index)
                do {
                    manifests[depot] = try download(depot: depot, account: account!, index: index)
                } catch let error as DownloadFailure where error.tokenRejected && !usedQRRetry {
                    try? fileManager.removeItem(at: paths.steamAccount)
                    account = try authorizeWithQR()
                    usedQRRetry = true
                    showDownload(depot: depot, index: index)
                    manifests[depot] = try download(depot: depot, account: account!, index: index)
                }
            }

            guard let contentManifest = manifests[contentDepot],
                  let engineManifest = manifests[engineDepot] else {
                try fail("DepotDownloader did not report both required manifests.")
            }
            try checkCancellation()
            try promote(contentManifest: contentManifest, engineManifest: engineManifest)
            try checkCancellation()
            finishSuccess()
        } catch {
            finishFailure(error.localizedDescription)
        }
    }

    private func prepareRuntime() throws {
        try fileManager.createDirectory(at: paths.runtime, withIntermediateDirectories: true)
        try fileManager.createDirectory(
            at: paths.runtime.appendingPathComponent(".download", isDirectory: true),
            withIntermediateDirectories: true
        )
        try fileManager.createDirectory(at: paths.previous, withIntermediateDirectories: true)
        try fileManager.createDirectory(at: paths.helperHome, withIntermediateDirectories: true)
        guard chmod(paths.helperHome.path, S_IRWXU) == 0 else {
            try fail("Could not secure DepotDownloader's private home directory.")
        }
        let bundleExtract = paths.helperHome.appendingPathComponent("dotnet-bundle", isDirectory: true)
        try fileManager.createDirectory(at: bundleExtract, withIntermediateDirectories: true)
        _ = chmod(bundleExtract.path, S_IRWXU)
    }

    private func ensureDownloader() throws {
        if fileManager.isExecutableFile(atPath: paths.downloader.path),
           try sha256(of: paths.downloader) == downloaderExecutableSHA256 {
            return
        }

        DispatchQueue.main.async { [self] in
            detailLabel.stringValue = "Downloading the verified open-source DepotDownloader..."
            progress.isIndeterminate = true
            progress.startAnimation(nil)
            showWindow()
        }
        let archive = paths.runtime.appendingPathComponent(".DepotDownloader.zip")
        let extracted = paths.runtime.appendingPathComponent(".DepotDownloader.extract", isDirectory: true)
        try? fileManager.removeItem(at: archive)
        try? fileManager.removeItem(at: extracted)
        do {
            let data = try Data(contentsOf: downloaderURL)
            try checkCancellation()
            guard Self.sha256(data) == downloaderArchiveSHA256 else {
                try fail("The DepotDownloader archive failed its pinned SHA-256 check. Nothing was executed.")
            }
            try data.write(to: archive, options: .atomic)
            try runCopy(
                executable: "/usr/bin/ditto",
                arguments: ["-x", "-k", archive.path, extracted.path]
            )
            let candidate = extracted.appendingPathComponent("DepotDownloader")
            guard regularFileExists(candidate),
                  try sha256(of: candidate) == downloaderExecutableSHA256 else {
                try fail("The extracted DepotDownloader executable failed verification.")
            }
            try? fileManager.removeItem(at: paths.tools)
            try fileManager.moveItem(at: extracted, to: paths.tools)
            guard chmod(paths.downloader.path, S_IRWXU) == 0 else {
                try fail("Could not make the verified DepotDownloader executable runnable.")
            }
        } catch {
            try? fileManager.removeItem(at: extracted)
            throw error
        }
        try? fileManager.removeItem(at: archive)
    }

    private static func sha256(_ data: Data) -> String {
        SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }

    private func sha256(of url: URL) throws -> String {
        Self.sha256(try Data(contentsOf: url, options: .mappedIfSafe))
    }

    private func checkCancellation() throws {
        if isCancelled() { try fail("Preparation was canceled.") }
    }

    private func loadUsableAccount() throws -> String? {
        guard let raw = try? String(contentsOf: paths.steamAccount, encoding: .utf8) else {
            return nil
        }
        let name = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, name.range(of: #"^[A-Za-z0-9_]+$"#, options: .regularExpression) != nil,
              validStoredCredentialPath() != nil else {
            try? fileManager.removeItem(at: paths.steamAccount)
            return nil
        }
        do {
            guard try KeychainStore.read() != nil else {
                try? fileManager.removeItem(at: paths.steamAccount)
                return nil
            }
        } catch {
            // A rebuilt ad-hoc local app may no longer satisfy the old item's
            // ACL. Never fall back to a password; start a fresh QR flow.
            try? fileManager.removeItem(at: paths.steamAccount)
            return nil
        }
        guard chmod(paths.steamAccount.path, S_IRUSR | S_IWUSR) == 0 else {
            try fail("Could not secure the saved Steam account name.")
        }
        return name
    }

    private func authorizeWithQR() throws -> String {
        try checkCancellation()
        showAuthorization()
        try fileManager.createDirectory(at: paths.authCheck, withIntermediateDirectories: true)
        let arguments = [
            "-app", "440", "-depot", "441", "232251",
            "-os", "windows", "-osarch", "64", "-manifest-only",
            "-qr", "-remember-password", "-dir", paths.authCheck.path
        ]
        let result = try runDownloader(arguments: arguments, configure: { capture in
            capture.onQR = { [weak self] text in self?.updateQR(text) }
        })
        try checkCancellation()
        guard result.status == 0,
              result.capture.manifests[contentDepot] != nil,
              result.capture.manifests[engineDepot] != nil else {
            try fail("Steam authorization did not grant access to both required TF2 depots. Scan the QR code with the official Steam Mobile app and approve the request.")
        }
        let name = try result.capture.accountName ?? requestAccountName()
        try writeText(name + "\n", to: paths.steamAccount, mode: S_IRUSR | S_IWUSR)
        return name
    }

    private func requestAccountName() throws -> String {
        var accountName: String?
        DispatchQueue.main.sync {
            let alert = NSAlert()
            alert.messageText = "Steam account name"
            alert.informativeText = "Steam approved the QR sign-in, but DepotDownloader did not report the account name needed to reuse its Keychain token. Enter only the public account name, never your password."
            let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 320, height: 24))
            field.placeholderString = "Account name"
            alert.accessoryView = field
            alert.addButton(withTitle: "Continue")
            alert.addButton(withTitle: "Cancel")
            if alert.runModal() == .alertFirstButtonReturn {
                accountName = field.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
            }
        }
        guard let name = accountName, !name.isEmpty,
              name.range(of: #"^[A-Za-z0-9_]+$"#, options: .regularExpression) != nil else {
            try fail("A valid Steam account name is required to reuse the approved Keychain token.")
        }
        return name
    }

    private struct DownloadFailure: LocalizedError {
        let tokenRejected: Bool
        let message: String

        var errorDescription: String? { message }
    }

    private func download(depot: Int, account: String, index: Int) throws -> UInt64 {
        try checkCancellation()
        let work = paths.work(for: depot)
        let complete = work.appendingPathComponent(completionFile)
        let hasCompletion = regularFileExists(complete) &&
            regularFileExists(paths.marker(for: depot, in: work))
        let previousManifest = hasCompletion ? completionManifest(depot: depot, root: work) : nil
        var arguments = [
            "-app", "440", "-depot", String(depot),
            "-os", "windows", "-osarch", "64", "-dir", work.path,
            "-username", account, "-remember-password"
        ]
        if !hasCompletion { arguments.append("-validate") }

        // Once DepotDownloader starts changing the work tree, only its own
        // interruption-safe config can establish completeness again.
        try? fileManager.removeItem(at: complete)
        let result = try runDownloader(arguments: arguments, configure: { [weak self] capture in
            capture.onActivity = { self?.revealDownload() }
            capture.onManifest = { _, manifest in
                if previousManifest != manifest { self?.revealDownload() }
            }
            capture.onProgress = { percent in
                self?.updateProgress(percent, index: index)
                if !hasCompletion { self?.revealDownload() }
            }
        })
        try checkCancellation()

        guard result.status == 0,
              result.capture.downloadedDepots.contains(depot),
              let manifest = result.capture.manifests[depot],
              regularFileExists(paths.marker(for: depot, in: work)) else {
            if result.capture.tokenRejected || result.capture.authorizationRejected {
                throw DownloadFailure(tokenRejected: true, message: "Steam rejected the saved sign-in token.")
            }
            throw DownloadFailure(
                tokenRejected: false,
                message: "TF2 depot \(depot) did not complete. The partial download was kept and will resume on the next attempt."
            )
        }
        try writeText(String(manifest) + "\n", to: complete)
        return manifest
    }

    private func prepareWorkDirectory(for depot: Int) throws {
        try checkCancellation()
        let work = paths.work(for: depot)
        if fileExists(work) { return }
        let final = paths.final(for: depot)
        if fileExists(final) {
            do {
                try runCopy(executable: "/bin/cp", arguments: ["-Rc", final.path, work.path])
            } catch {
                try? fileManager.removeItem(at: work)
                do {
                    try runCopy(executable: "/usr/bin/ditto", arguments: [final.path, work.path])
                } catch {
                    try? fileManager.removeItem(at: work)
                    throw error
                }
            }
        } else {
            try fileManager.createDirectory(at: work, withIntermediateDirectories: true)
        }
    }

    private func runCopy(executable: String, arguments: [String]) throws {
        try checkCancellation()
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = FileHandle.standardOutput
        process.standardError = FileHandle.standardOutput
        defer { setActiveProcess(nil) }
        do {
            try process.run()
            setActiveProcess(process)
        } catch {
            try fail("Could not start \(executable): \(error.localizedDescription)")
        }
        process.waitUntilExit()
        try checkCancellation()
        if process.terminationStatus != 0 {
            try fail("Could not copy the existing TF2 depot into the resumable work directory.")
        }
    }

    private func runDownloader(
        arguments: [String],
        configure: (OutputCapture) -> Void
    ) throws -> ChildResult {
        try checkCancellation()
        let process = Process()
        let capture = OutputCapture()
        configure(capture)
        guard fileManager.isExecutableFile(atPath: paths.downloader.path),
              try sha256(of: paths.downloader) == downloaderExecutableSHA256 else {
            try fail("DepotDownloader changed after verification. Credentials were not released.")
        }
        process.executableURL = paths.downloader
        process.arguments = arguments
        process.currentDirectoryURL = paths.helperHome
        var environment = ProcessInfo.processInfo.environment
        environment["HOME"] = paths.helperHome.path
        environment["XDG_DATA_HOME"] = paths.helperHome
            .appendingPathComponent("xdg-data", isDirectory: true).path
        environment["DOTNET_BUNDLE_EXTRACT_BASE_DIR"] = paths.helperHome
            .appendingPathComponent("dotnet-bundle", isDirectory: true).path
        process.environment = environment
        process.standardInput = FileHandle.nullDevice

        let output = Pipe()
        let error = Pipe()
        process.standardOutput = output
        process.standardError = error
        let logLock = NSLock()
        let outputReader = LineReader(
            isStandardOutput: true, capture: capture, logLock: logLock
        )
        let errorReader = LineReader(
            isStandardOutput: false, capture: capture, logLock: logLock
        )
        let readers = DispatchGroup()

        func installReader(_ handle: FileHandle, reader: LineReader) {
            readers.enter()
            handle.readabilityHandler = { readable in
                let data = readable.availableData
                if data.isEmpty {
                    readable.readabilityHandler = nil
                    reader.finish()
                    readers.leave()
                    return
                }
                reader.append(data)
            }
        }

        installReader(output.fileHandleForReading, reader: outputReader)
        installReader(error.fileHandleForReading, reader: errorReader)

        var credentialError: Error?
        do {
            try restoreCredentialFile()
            try process.run()
            setActiveProcess(process)
            process.waitUntilExit()
        } catch {
            credentialError = error
        }
        setActiveProcess(nil)
        output.fileHandleForWriting.closeFile()
        error.fileHandleForWriting.closeFile()
        readers.wait()

        do {
            try captureCredentialFile(required: true)
        } catch {
            credentialError = credentialError ?? error
        }
        if let credentialError { throw credentialError }
        return ChildResult(status: process.terminationStatus, capture: capture)
    }

    private func validStoredCredentialPath() -> URL? {
        guard let relativeRaw = try? String(contentsOf: paths.authConfigPath, encoding: .utf8) else {
            return nil
        }
        let relative = relativeRaw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !relative.isEmpty, !relative.hasPrefix("/"), !relative.split(separator: "/").contains("..") else {
            return nil
        }
        let candidate = paths.helperHome.appendingPathComponent(relative).standardizedFileURL
        let prefix = paths.helperHome.standardizedFileURL.path + "/"
        return candidate.path.hasPrefix(prefix) && candidate.lastPathComponent == "account.config"
            ? candidate : nil
    }

    private func restoreCredentialFile() throws {
        guard let destination = validStoredCredentialPath(), let data = try KeychainStore.read() else {
            return
        }
        try fileManager.createDirectory(
            at: destination.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try writeAtomically(data, to: destination, mode: S_IRUSR | S_IWUSR)
    }

    private func captureCredentialFile(required: Bool) throws {
        guard let enumerator = fileManager.enumerator(
            at: paths.helperHome,
            includingPropertiesForKeys: [.contentModificationDateKey, .isRegularFileKey],
            options: []
        ) else {
            try fail("Could not inspect DepotDownloader's private credential storage.")
        }
        var candidates: [(URL, Date)] = []
        for case let url as URL in enumerator where url.lastPathComponent == "account.config" {
            let values = try? url.resourceValues(forKeys: [
                URLResourceKey.contentModificationDateKey,
                URLResourceKey.isRegularFileKey
            ])
            if values?.isRegularFile == true {
                candidates.append((url, values?.contentModificationDate ?? .distantPast))
            }
        }
        guard let selected = candidates.max(by: { $0.1 < $1.1 })?.0 else {
            if required {
                try fail("DepotDownloader did not create its isolated credential file.")
            }
            return
        }
        var firstError: Error?
        do {
            let data = try Data(contentsOf: selected)
            try KeychainStore.write(data)
            let root = paths.helperHome.standardizedFileURL.path + "/"
            let full = selected.standardizedFileURL.path
            guard full.hasPrefix(root) else {
                try fail("DepotDownloader placed its credential file outside its private home.")
            }
            try writeText(String(full.dropFirst(root.count)) + "\n", to: paths.authConfigPath)
        } catch {
            firstError = error
        }
        for (url, _) in candidates {
            do { try fileManager.removeItem(at: url) } catch {
                firstError = firstError ?? error
            }
        }
        if let firstError { throw firstError }
    }

    private func completionManifest(depot: Int, root: URL) -> UInt64? {
        let file = root.appendingPathComponent(completionFile)
        guard regularFileExists(paths.marker(for: depot, in: root)),
              let text = try? String(contentsOf: file, encoding: .utf8) else {
            return nil
        }
        return UInt64(text.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    private func recoverPromotionIfNeeded() throws {
        guard let transaction = decodeState(at: paths.promoting),
              validTransaction(transaction) else {
            if fileExists(paths.promoting) {
                try fail("The interrupted TF2 content promotion record is invalid. The existing depots were left untouched.")
            }
            return
        }
        do {
            try finishPromotion(transaction)
        } catch {
            try rollbackPromotion()
        }
    }

    private func rollbackPromotion() throws {
        for depot in [contentDepot, engineDepot] {
            let final = paths.final(for: depot)
            let work = paths.work(for: depot)
            let old = paths.previous.appendingPathComponent("depot-\(depot)", isDirectory: true)
            guard fileExists(old) else { continue }
            if fileExists(final) {
                if fileExists(work) {
                    try fileManager.removeItem(at: final)
                } else {
                    try fileManager.moveItem(at: final, to: work)
                }
            }
            try fileManager.moveItem(at: old, to: final)
        }
        guard let contentManifest = completionManifest(depot: contentDepot, root: paths.contentFinal),
              let engineManifest = completionManifest(depot: engineDepot, root: paths.engineFinal) else {
            try fail("The interrupted TF2 content update could not be completed or rolled back safely.")
        }
        let staleState = SavedState(
            app: appID,
            contentDepot: contentDepot,
            contentManifest: contentManifest,
            engineDepot: engineDepot,
            engineManifest: engineManifest,
            verified: true,
            checkedAt: "1970-01-01T00:00:00Z"
        )
        try writeState(staleState, to: paths.state)
        try fileManager.removeItem(at: paths.promoting)
    }

    private func validTransaction(_ state: SavedState) -> Bool {
        state.verified && state.app == appID && state.contentDepot == contentDepot &&
            state.engineDepot == engineDepot && state.contentManifest > 0 && state.engineManifest > 0
    }

    private func promote(contentManifest: UInt64, engineManifest: UInt64) throws {
        let state = SavedState(
            app: appID,
            contentDepot: contentDepot,
            contentManifest: contentManifest,
            engineDepot: engineDepot,
            engineManifest: engineManifest,
            verified: true,
            checkedAt: ISO8601DateFormatter().string(from: Date())
        )
        guard completionManifest(depot: contentDepot, root: paths.contentWork) == contentManifest,
              completionManifest(depot: engineDepot, root: paths.engineWork) == engineManifest else {
            try fail("The verified TF2 work directories changed before promotion.")
        }
        try writeState(state, to: paths.promoting)
        if fileExists(paths.state) {
            try fileManager.removeItem(at: paths.state)
        }
        try finishPromotion(state)
    }

    private func finishPromotion(_ state: SavedState) throws {
        try promoteDepot(contentDepot, manifest: state.contentManifest)
        try promoteDepot(engineDepot, manifest: state.engineManifest)
        guard completionManifest(depot: contentDepot, root: paths.contentFinal) == state.contentManifest,
              completionManifest(depot: engineDepot, root: paths.engineFinal) == state.engineManifest else {
            try fail("The TF2 depots could not be verified after promotion.")
        }
        let finalState = SavedState(
            app: state.app,
            contentDepot: state.contentDepot,
            contentManifest: state.contentManifest,
            engineDepot: state.engineDepot,
            engineManifest: state.engineManifest,
            verified: true,
            checkedAt: ISO8601DateFormatter().string(from: Date())
        )
        try writeState(finalState, to: paths.state)
        try fileManager.removeItem(at: paths.promoting)
        try? fileManager.removeItem(at: paths.previous)
        try fileManager.createDirectory(at: paths.previous, withIntermediateDirectories: true)
    }

    private func promoteDepot(_ depot: Int, manifest: UInt64) throws {
        try checkCancellation()
        let final = paths.final(for: depot)
        let work = paths.work(for: depot)
        if completionManifest(depot: depot, root: work) != manifest {
            if completionManifest(depot: depot, root: final) == manifest { return }
            try fail("The interrupted promotion for TF2 depot \(depot) cannot be completed safely.")
        }
        let old = paths.previous.appendingPathComponent("depot-\(depot)", isDirectory: true)
        if fileExists(final) {
            if fileExists(old) { try fileManager.removeItem(at: old) }
            try fileManager.moveItem(at: final, to: old)
        }
        try fileManager.moveItem(at: work, to: final)
    }

    private func writeState(_ state: SavedState, to url: URL) throws {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        var data = try encoder.encode(state)
        data.append(10)
        try writeAtomically(data, to: url)
    }

    private func finishSuccess() {
        DispatchQueue.main.async { [self] in
            exitCode = 0
            window.orderOut(nil)
            NSApp.stop(nil)
            postWakeEvent()
        }
    }

    private func finishFailure(_ message: String) {
        let line = "error: \(message)\n"
        if let data = line.data(using: .utf8) {
            try? FileHandle.standardOutput.write(contentsOf: data)
        }
        DispatchQueue.main.async { [self] in
            detailLabel.stringValue = message
            progress.stopAnimation(nil)
            cancelButton.isEnabled = false
            showWindow()
            let alert = NSAlert()
            alert.alertStyle = .critical
            alert.messageText = "Team Frontress could not prepare TF2 content"
            alert.informativeText = message
            alert.addButton(withTitle: "OK")
            alert.runModal()
            exitCode = 1
            window.orderOut(nil)
            NSApp.stop(nil)
            postWakeEvent()
        }
    }

    private func postWakeEvent() {
        if let event = NSEvent.otherEvent(
            with: .applicationDefined,
            location: .zero,
            modifierFlags: [],
            timestamp: 0,
            windowNumber: 0,
            context: nil,
            subtype: 0,
            data1: 0,
            data2: 0
        ) {
            NSApp.postEvent(event, atStart: false)
        }
    }
}

private func runMain() -> Never {
    guard let options = Options.parse(CommandLine.arguments) else {
        fputs("usage: tf2-content-helper\n", stderr)
        exit(2)
    }

    let paths = RuntimePaths(support: options.support)
    let application = NSApplication.shared
    let controller = HelperController(options: options, paths: paths)
    application.delegate = controller
    application.setActivationPolicy(.accessory)
    application.run()
    exit(controller.exitCode)
}

runMain()
