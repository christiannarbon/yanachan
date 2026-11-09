/*
 * Japanese message catalog.
 *
 * Typed as `Messages`, so vue-tsc fails the build if a key from en.ts is
 * missing here or an interpolating function takes the wrong arguments. Keys
 * stay in en.ts order to make the two files easy to read side by side.
 *
 * Identifiers users type or read verbatim -- gh, GH_TOKEN, org/team-slug,
 * @logins -- are left in Latin script; only the prose around them is
 * translated.
 */

import type { Messages } from './en'

const ja: Messages = {
  appName: 'Yana-chan 4K',

  common: {
    loading: '読み込み中です。',
    somethingWrong: '問題が発生しました。',
    remove: '削除',
    copy: 'コピー',
    close: '閉じる',
  },

  time: {
    justNow: 'たった今',
    minutes: (n: number) => `${n} 分前`,
    hours: (n: number) => `${n} 時間前`,
    days: (n: number) => `${n} 日前`,
    dateLocale: 'ja-JP',
    /** ja-JP renders this as "8月28日(金) 09:12". */
    dateFormat: {
      month: 'long',
      day: 'numeric',
      weekday: 'short',
      hour: '2-digit',
      minute: '2-digit',
    },
    listSeparator: '、',
    /** ja-JP renders this as "月"…"日". */
    weekdayFormat: { weekday: 'short' },
    /** ja-JP renders this as "8月24日". */
    dayFormat: { month: 'long', day: 'numeric' },
  },

  window: {
    fixed: (hours: number) => `直近 ${hours} 時間`,
    businessDay: '前営業日（金）以降',
  },

  sections: {
    dashboard: '今週の成果',
    mine: '自分のオープン PR',
    review: 'レビュー依頼を受けた PR',
  },

  nav: {
    label: 'セクション',
    queues: 'キュー',
    teams: 'チーム',
    orgs: '組織',
    open: 'セクション',
    close: '閉じる',
  },

  board: {
    summary: '{login} · 対象期間: {label}（{stamp} 以降）',
    needingAttention: (n: number) => `対応が必要 ${n} 件`,
    prCount: (n: number) => `プルリクエスト ${n} 件`,
    repoCount: (n: number) => `リポジトリ ${n} 件`,
    hiddenAsQuiet: (n: number) => `動きなしのため非表示 ${n} 件`,
    showingActiveOnly: '動きのあるものだけ表示中',
    showingEverything: 'すべて表示中',
    collapseAll: 'すべて折りたたむ',
    expandAll: 'すべて展開',
    autoRefresh: '自動更新',
    refresh: '更新',
    refreshing: '更新中',
    githubReported: (warning: string) => `GitHub からの報告: ${warning}`,
    troubleTitle: 'GitHub に接続できませんでした。',
    tryAgain: 'もう一度試す',
    empty: 'ここには何もありません。',
    emptyScope: 'このセクションの対象は {ref} です。',
    footer: (generated: string, limit: number) =>
      `生成: ${generated} · 1 クエリあたり ${limit} 件取得`,
  },

  dashboard: {
    range: (from: string, to: string) => `${from} 〜 ${to}`,

    kcalUnit: 'kcal',
    kcalCaption: '今週の消費カロリー',
    kcalHow: '作成 200 · マージ 400 · クローズ 100 · レビュー 150 · 承認 50',

    opened: '作成',
    merged: 'マージ',
    closed: 'クローズ',
    reviewed: 'レビュー',
    openedNote: '自分が作成した PR',
    mergedNote: 'マージまで到達',
    closedNote: 'マージせずクローズ',
    reviewedNote: 'レビューした PR',

    chartTitle: '日ごとの動き',
    chartNote: '作成・マージ・レビューを共通の目盛りで並べています。',
    chartEmpty: 'まだ描くものがありません。',
    chartTable: '期間内の各日の活動。',
    columnDay: '日付',

    reviewsWritten: (n: number) => `レビュー ${n} 件`,
    approvalsGiven: (n: number) => `承認 ${n} 件`,
    reposTouched: (n: number) => `リポジトリ ${n} 件`,
    linesMerged: (added: string, removed: string) => `マージした差分 +${added} / −${removed} 行`,
    filesChanged: (n: number) => `変更ファイル ${n} 件`,
    activeDays: (active: number, total: number) => `${total} 日中 ${active} 日活動`,
    streak: (n: number) => `${n} 日連続`,

    highlightsTitle: '今週のハイライト',
    fastestMerge: '最速マージ',
    biggestMerge: '最大の差分',
    busiestRepo: '最も動いたリポジトリ',
    busiestDay: '最も動いた日',
    duration: (minutes: number) => {
      if (minutes < 1) return '1 分未満'
      if (minutes < 60) return `${minutes} 分`
      if (minutes < 60 * 24) return `${Math.round(minutes / 60)} 時間`
      return `${Math.round(minutes / (60 * 24))} 日`
    },
    lineCount: (lines: string) => `${lines} 行`,
    prCount: (n: number) => `PR ${n} 件`,

    quietTitle: '静かな一週間でした。',
    quietBody:
      'この期間には作成・マージ・レビューがありませんでした。ほかのセクションに、待っているものがあります。',
  },

  pr: {
    statusReply: '返信あり',
    statusNew: '新着',
    draft: '下書き',
    checksSuccess: 'チェック成功',
    checksFailure: 'チェック失敗',
    checksPending: 'チェック実行中',
    approved: '承認済み',
    changesRequested: '変更依頼',
    reviewRequired: 'レビュー必須',
    youApproved: 'あなたが承認',
    youRequestedChanges: 'あなたが変更を依頼',
    youCommented: 'あなたがコメント',
    alsoRequestedFromYou: 'あなたにも依頼あり',
    awaitingYou: 'あなたの対応待ち',
    awaitingTeam: 'チームのレビュー待ち',
    answered: (actors: string) => `${actors} が返信`,
    bots: (actors: string) => `${actors}（ボット）`,
    commentsFrom: (n: number, actors: string) => `${actors} からコメント ${n} 件`,
    botUpdates: (n: number) => `ボットの更新 ${n} 件`,
    quietSince: (last: string) => `新しいコメントなし · 最終更新: ${last}`,
    noComments: 'まだコメントはありません',
    noActivityInWindow: '対象期間内に新しい動きはありません',
    byline: (author: string, opened: string) => ` · @${author} · 作成: ${opened}`,
    partSeparator: ' · ',
  },

  auth: {
    intro:
      'ダッシュボードが GitHub にアクセスする方法を選んでください。いずれかを承認するまで、何も読み取りません。',

    cliTitle: 'このマシンの gh CLI を使う',
    cliDetected: '検出済み',
    cliNotLoggedIn: '未ログイン',
    cliNotFound: '未検出',
    cliSession: 'gh は {path} にインストールされ、{host} に {login} としてログインしています。',
    cliSessionNoLogin: 'gh は {path} にインストールされ、{host} にログインしています。',
    cliExplain:
      '承認すると、ダッシュボードは {command} でそのセッションのトークンを読み取り、すべての GitHub API 呼び出しに使います。トークンはこのマシンの state ディレクトリに留まり、送信先は GitHub だけです。',
    cliApprove: '承認して gh CLI のセッションを使う',
    cliChecking: 'トークンを確認中',
    cliNoAccount: 'gh はインストールされていますが、ログイン中のアカウントがありません。',
    cliRunLogin: 'ターミナルで {command} を実行し、このページを再読み込みしてください。',
    cliMissing: 'gh CLI は PATH 上に見つかりませんでした。',
    cliContainerHint:
      'ダッシュボードをコンテナ内で動かしている場合は想定どおりです。下の OAuth を使うか、{variable} でトークンを渡してください。',

    envTitle: '環境から渡されたトークンを使う',
    envAvailable: '利用可能',
    envExplain:
      '{variable} でトークンが渡されています。{command} はこの仕組みでローカルの gh セッションをコンテナへ引き渡します。承認すると、そのトークンを使います。',
    envApprove: '承認して渡されたトークンを使う',

    oauthTitle: 'GitHub でサインイン',
    oauthEnabled: 'OAuth デバイスフロー',
    oauthNotConfigured: '未設定',
    oauthSetup:
      'デバイスフローを有効にした OAuth アプリのクライアント ID を {variable} に設定し、バックエンドを再起動してください。',
    oauthScopes:
      '要求するスコープは {scopes} です。GitHub がブラウザで入力するコードを表示します。クライアントシークレットやコールバック URL は使いません。',
    oauthStart: 'デバイスサインインを開始',
    oauthContacting: 'GitHub に接続中',
    oauthOpen: '{link} を開き、次のコードを入力してください:',
    oauthWaiting: 'github.com でコードが承認されるのを待っています',
    oauthApproved: '承認されました',
    oauthExpired: '承認される前にデバイスコードの有効期限が切れました。もう一度お試しください。',
    oauthDenied: 'github.com で認可が拒否されました。',
    oauthCopied: 'コードをクリップボードにコピーしました',
    oauthCopyManually: 'コードを手動でコピーしてください',

    serverIntro: 'GitHub アカウントでサインインすると、レビュー待ちの一覧を確認できます。',
    serverTitle: 'GitHub でサインイン',
    serverExplain:
      'GitHub に移動してアクセスを承認すると、このページに戻ります。ダッシュボードが使うのはあなた自身の権限だけで、用途はプルリクエストの読み取りに限られます。',
    serverSignIn: 'GitHub でサインイン',
    serverLeaving: 'GitHub を開いています',
    errDenied: 'GitHub でサインインがキャンセルされました。',
    errState: 'サインインに時間がかかりすぎたか、別のタブで開始されました。もう一度お試しください。',
    errExchange:
      'GitHub がサインインを受け付けませんでした。もう一度お試しください。繰り返し失敗する場合は、このダッシュボードの管理者に GitHub アプリの設定を確認してもらってください。',
    errNotAllowed:
      'この GitHub アカウントではダッシュボードを利用できません。どの Organization に所属する必要があるか、管理者に確認してください。',
    errServer: 'サインイン中に問題が発生しました。もう一度お試しください。',
    reauthPending: 'GitHub のサインインを更新しています…',
    reauthStuck: 'GitHub のサインインを更新できませんでした。',
    reauthStuckHint: 'もう一度更新するか、サインアウトしてください。',
    reauthRetry: 'サインインを更新',
  },

  settings: {
    title: '設定',
    saved: '保存しました。新しいセクションでボードを再読み込みします。',
    teamFormat: 'チームは org/team-slug の形式で入力してください。',
    orgFormat: 'Organization はスラッシュを含まないログイン名です。',

    teamsTitle: '追跡するチーム',
    teamsExplain:
      'チームごとにセクションができ、そのチームにレビューが依頼されているオープンなプルリクエストを一覧します。{format} の形式で入力してください。ここに追加するまで、何も追跡しません。',
    teamsEmpty: '追跡中のチームはありません。',
    teamAdd: 'チームを追加',
    teamsYours: 'あなたのチーム:',

    orgsTitle: '追跡する Organization',
    orgsExplain:
      'Organization ごとにセクションができ、その Organization のうちあなたが関わっており、他のセクションにまだ出ていないオープンなプルリクエストを一覧します。',
    orgsEmpty: '追跡中の Organization はありません。',
    orgAdd: 'Organization を追加',
    orgsYours: 'あなたの Organization:',
    membershipsLoading: '所属情報を読み込み中です。',
    membershipsFailed: (warning: string) => `所属情報を取得できませんでした: ${warning}`,

    viewTitle: '表示',
    limitLabel: '1 クエリあたりの取得件数',
    limitHint: '1 から 100 まで。スクリプトの {flag} に対応します。',
    windowLabel: '対象期間',
    windowHint: '時間単位。0 のままにすると営業日ルール（月曜は金曜まで遡る）を使います。',
    onlyActive: '対象期間内に新しい動きがないプルリクエストを隠す',
    showUrls: '各プルリクエストの下に完全な URL を表示する',
    save: '設定を保存',
    saving: '保存中',

    sessionTitle: 'セッション',
    sessionLine: '{login} として、{mode}でサインインしています。',
    modeGhCli: 'ローカルの gh CLI セッション',
    modeOauth: 'OAuth デバイスフロー',
    modeEnvToken: '環境変数のトークン',
    signOut: 'サインアウトしてトークンを破棄',
    modeGithub: 'GitHub アカウント',
    signOutServer: 'サインアウト',
  },

  theme: {
    label: 'テーマ',
    current: (name: string) => `テーマ: ${name}`,
    groupOde: 'ヤナミへのオード',
    groupHouse: '標準',
    groupPainting: '絵画',
  },

  locale: {
    label: '言語',
    en: 'English',
    ja: '日本語',
  },
}

export default ja
