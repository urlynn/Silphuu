// Comment preview
reinitOnSwap(function initPreview() {
    var btn = document.getElementById('cmt-preview-btn');
    if (!btn || btn._previewInit) return;
    btn._previewInit = true;
    var ta = document.getElementById('cmt-content');
    var preview = document.getElementById('cmt-preview');
    var visible = false;
    if (!ta || !preview) return;

    btn.addEventListener('click', function() {
        visible = !visible;
        var eye = document.getElementById('cmt-preview-eye');
        var eyeOff = document.getElementById('cmt-preview-eye-off');
        if (visible) {
            preview.style.display = 'block';
            if (eye) eye.style.display = 'none';
            if (eyeOff) eyeOff.style.display = '';
            renderPreview();
        } else {
            preview.style.display = 'none';
            if (eye) eye.style.display = '';
            if (eyeOff) eyeOff.style.display = 'none';
        }
    });

    function renderPreview() {
        var body = document.getElementById('cmt-preview-body');
        if (!body) return;
        var md = ta.value;
        var html = md
            .replace(/&/g, '&amp;').replace(/</g, '&lt;')
            .replace(/^### (.+)$/gm, '<h3>$1</h3>')
            .replace(/^## (.+)$/gm, '<h2>$1</h2>')
            .replace(/^# (.+)$/gm, '<h1>$1</h1>')
            .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
            .replace(/\*(.+?)\*/g, '<em>$1</em>')
            .replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>')
            .replace(/`([^`]+)`/g, '<code>$1</code>')
            .replace(/!\[([^\]]*)\]\(([^)]+)\)/g, '<img src="$2" alt="$1">')
            .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank">$1</a>')
            .replace(/^> (.+)$/gm, '<blockquote>$1</blockquote>')
            .replace(/^- (.+)$/gm, '<li>$1</li>')
            .replace(/(<li>[\s\S]*?)(<\/li>\n?)+/g, '<ul>$1</ul>')
            .replace(/\n\n/g, '</p><p>');
        html = '<p>' + html + '</p>';
        html = html.replace(/<p>\s*<\/p>/g, '');
        body.innerHTML = html;
    }

    // Auto-refresh the preview on input
    ta.addEventListener('input', function() {
        if (visible) renderPreview();
    });

    // Auto-height textarea
    ta.addEventListener('input', function() {
        this.style.height = 'auto';
        this.style.height = this.scrollHeight + 'px';
    });
});

// Reply mode
// Cancel reply: live-query all elements and move the form back to its original spot
// (no closure references, safe after htmx swaps)
function _cmtCancelReply() {
    var formCard = document.getElementById('cmt-form-card');
    if (!formCard || !formCard._isReplyMode) return;
    // Restore anchor = before the glass panel (.cmt-glass-panel): the form's default
    // position sits above the glass (the glass wraps only "fixed header + dynamic
    // list"; see the post.html/cmt.css comments)
    var anchorBefore = document.querySelector('.cmt-section .cmt-glass-panel') || document.getElementById('cmt-comment-area');
    if (anchorBefore && anchorBefore.parentNode) {
        anchorBefore.parentNode.insertBefore(formCard, anchorBefore);
    }
    var ridInput = document.getElementById('cmt-rid');
    var textarea = document.getElementById('cmt-content');
    var closeBtn = document.getElementById('cmt-cancel-reply');
    if (ridInput) ridInput.value = '';
    if (textarea) textarea.placeholder = formCard._origPlaceholder || '欢迎评论';
    if (closeBtn) closeBtn.style.display = 'none';
    formCard._isReplyMode = false;
}

// document-level .cmt-reply-btn handler: bound once, live-queries form elements
if (!document._replyClickInit) {
    document._replyClickInit = true;
    document.addEventListener('click', function(e) {
        var btn = e.target.closest('.cmt-reply-btn');
        if (!btn) return;
        e.preventDefault();

        var formCard = document.getElementById('cmt-form-card');
        var ridInput = document.getElementById('cmt-rid');
        var textarea = document.getElementById('cmt-content');
        var closeBtn = document.getElementById('cmt-cancel-reply');
        if (!formCard || !ridInput || !textarea || !closeBtn) return;

        var id = btn.dataset.id;
        var nick = btn.dataset.nick || '';
        var item = btn.closest('.cmt-item');
        if (!item) return;

        // Cancel any previous reply state first (if already in reply mode)
        _cmtCancelReply();

        // Insert the form card after the target comment
        if (item.nextSibling) {
            item.parentNode.insertBefore(formCard, item.nextSibling);
        } else {
            item.parentNode.appendChild(formCard);
        }

        ridInput.value = id;
        textarea.placeholder = '@' + nick;
        // Insert @nick into the textarea
        var prefix = '@' + nick + ' ';
        if (textarea.value.indexOf(prefix) !== 0) {
            textarea.value = prefix + textarea.value;
        }
        closeBtn.style.display = '';
        formCard._isReplyMode = true;

        // Scroll to the form
        formCard.scrollIntoView({ behavior: 'smooth', block: 'center' });
        textarea.focus();
    });
}

// closeBtn binding + origPlaceholder init: rebind after htmx swaps
reinitOnSwap(function initReplyCancel() {
    var closeBtn = document.getElementById('cmt-cancel-reply');
    var formCard = document.getElementById('cmt-form-card');
    var textarea = document.getElementById('cmt-content');
    if (!closeBtn || !formCard || !textarea) return;
    if (closeBtn._cancelInit) return;
    closeBtn._cancelInit = true;

    // Store origPlaceholder (the server-rendered default)
    formCard._origPlaceholder = textarea.getAttribute('placeholder') || '欢迎评论';

    closeBtn.addEventListener('click', function(e) {
        e.preventDefault();
        _cmtCancelReply();
    });
});

