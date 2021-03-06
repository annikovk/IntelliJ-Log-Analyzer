let IndexingDiagnosticLinkHandler = async function (e) {
    let editor = e.editor
    let pos = editor.getCursorPosition()
    let token = editor.session.getTokenAt(pos.row, pos.column)
    if ((token.type !== null) && (/IndexingDiagnosticHyperlink/.test(token.type))) {
        await window.go.main.App.OpenIndexingReport(token.value)
    } else if ((token.type !== null) && (/IndexingProjectDiagnosticHyperlink/.test(token.type))) {
        let lineLength = editor.session.getLine(pos.row).length
        token = editor.session.getTokenAt(pos.row, lineLength-1)
        await window.go.main.App.OpenIndexingSummaryForProject(token.value)
    }

}